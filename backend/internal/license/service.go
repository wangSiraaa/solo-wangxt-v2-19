// Package license 是浮动许可证借出的核心业务层。
// 所有席位状态变更都在 MySQL InnoDB 事务内完成：
//   - 部门行锁把同部门并发借出串行化，事务内判断剩余额度；
//   - checkouts.active_seat 的唯一约束在数据库层兜底，
//     保证最后一个席位被并发申请时只有一人成功；
//   - (dept_id, request_id) 唯一约束实现幂等：网络重试拿到原借用结果。
package license

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"licensepool/internal/credential"
	"licensepool/internal/store"
)

// 业务错误码（HTTP 映射在 api 层）。
const (
	CodeNoSeat          = "NO_SEAT"           // 额度已满
	CodeBadCredential   = "BAD_CREDENTIAL"    // 凭证缺失/签名错误/格式错误
	CodeExpiredReclaim  = "EXPIRED_RECLAIMED" // 过期凭证：席位已由系统回收，不能凭它归还
	CodeNotFound        = "NOT_FOUND"
	CodeRequestConflict = "REQUEST_CONFLICT" // 同一 request_id 但参数不同
	CodeInvalidParam    = "INVALID_PARAM"
	CodeHeartbeatDenied = "HEARTBEAT_DENIED" // 离线凭证不支持在线心跳
)

// Error 携带错误码供 API 层映射 HTTP 状态。
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func errf(code, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// BorrowRequest 借出申请。
type BorrowRequest struct {
	DeptID    int64  `json:"deptId"`
	Employee  string `json:"employee"`
	RequestID string `json:"requestId"` // 幂等键，由客户端生成（如 UUID）
	Offline   bool   `json:"offline"`
	TTLSecond int64  `json:"ttlSecond"` // 借用时长
}

// BorrowResult 借出/重试返回体，内含签名凭证。
type BorrowResult struct {
	Replayed   bool      `json:"replayed"`
	Token      string    `json:"token"`
	Credential string    `json:"credential"` // 本地保存的签名模拟凭证
	SeatID     int64     `json:"seatId"`
	SeatSlot   int       `json:"seatSlot"`
	DeptID     int64     `json:"deptId"`
	Employee   string    `json:"employee"`
	Offline    bool      `json:"offline"`
	IssuedAt   time.Time `json:"issuedAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

// ReturnResult 归还结果（重复归还也返回结构化结果）。
type ReturnResult struct {
	Status     string     `json:"status"`  // returned / expired
	Already    bool       `json:"already"` // 是否为重复操作
	Reason     string     `json:"reason,omitempty"`
	Token      string     `json:"token"`
	SeatID     int64      `json:"seatId"`
	SeatSlot   int        `json:"seatSlot"`
	ReturnedAt *time.Time `json:"returnedAt,omitempty"`
}

const maxTTL = 24 * time.Hour

type Service struct {
	db     *sql.DB
	signer *credential.Signer
	now    func() time.Time // 可注入时钟，便于测试过期
}

func NewService(db *sql.DB, signer *credential.Signer) *Service {
	return &Service{db: db, signer: signer, now: time.Now}
}

// Borrow 借出一个席位。
func (s *Service) Borrow(ctx context.Context, req BorrowRequest) (*BorrowResult, error) {
	if req.DeptID <= 0 || req.Employee == "" || req.RequestID == "" {
		return nil, errf(CodeInvalidParam, "deptId、employee、requestId 均不能为空")
	}
	ttl := time.Duration(req.TTLSecond) * time.Second
	if ttl <= 0 {
		return nil, errf(CodeInvalidParam, "ttlSecond 必须为正数")
	}
	if ttl > maxTTL {
		return nil, errf(CodeInvalidParam, "借用时长不能超过 %s", maxTTL)
	}

	// 幂等快路径：request_id 已存在说明这是网络重试，直接返回原借用结果。
	if existing, err := s.findByRequest(ctx, req.DeptID, req.RequestID); err == nil {
		if existing.Employee != req.Employee {
			return nil, errf(CodeRequestConflict, "requestId 已被员工 %q 使用", existing.Employee)
		}
		return s.toBorrowResult(existing, true)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	// MySQL DATETIME(6) 精度为微秒。签发前先把时间截断到微秒，
	// 保证"网络重试"从数据库读回的时间与首次签发完全一致、凭证逐字节相同。
	issuedAt := s.now().Truncate(time.Microsecond)
	expiresAt := issuedAt.Add(ttl)

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// 1) 锁定部门行：同部门并发借出在此排队串行化。
	var quota int
	var deptName string
	err = tx.QueryRowContext(ctx,
		`SELECT id, name, quota FROM departments WHERE id=? FOR UPDATE`,
		req.DeptID).Scan(&req.DeptID, &deptName, &quota)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errf(CodeNotFound, "部门 %d 不存在", req.DeptID)
	}
	if err != nil {
		return nil, err
	}

	// 2) 准入判断：缩减额度只限制"新申请"，绝不抹掉现存借用。
	//    因此现存的所有活跃借用（含额度被缩减后的额度外遗留席位）
	//    都计入对新额度的占用：activeTotal >= quota 即拒绝新申请。
	var activeTotal int
	err = tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM checkouts
		WHERE dept_id=? AND status='active'`,
		req.DeptID).Scan(&activeTotal)
	if err != nil {
		return nil, err
	}
	if activeTotal >= quota {
		return nil, errf(CodeNoSeat, "部门 %q 额度 %d 已被现存借用占满（缩减额度只限制新申请）", deptName, quota)
	}

	// 3) 在额度内找一个未被活跃占用的席位并锁定它。
	var seatID int64
	var slotNo int
	err = tx.QueryRowContext(ctx, `
		SELECT s.id, s.slot_no FROM seats s
		WHERE s.dept_id=? AND s.slot_no < ?
		  AND NOT EXISTS (
		    SELECT 1 FROM checkouts c WHERE c.seat_id=s.id AND c.status='active'
		  )
		ORDER BY s.slot_no LIMIT 1
		FOR UPDATE OF s`,
		req.DeptID, quota).Scan(&seatID, &slotNo)
	if errors.Is(err, sql.ErrNoRows) {
		// 理论上不会发生（上面的计数已保证），作为防御性兜底。
		return nil, errf(CodeNoSeat, "没有可分配席位")
	}
	if err != nil {
		return nil, err
	}

	// 4) 写入借用记录（token + 幂等键 + 席位，均受唯一约束保护）。
	token := newToken()
	offline := 0
	if req.Offline {
		offline = 1
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO checkouts
		  (token, seat_id, dept_id, employee, request_id, offline, status, issued_at, expires_at)
		VALUES (?,?,?,?,?,?,'active',?,?)`,
		token, seatID, req.DeptID, req.Employee, req.RequestID, offline, issuedAt, expiresAt)
	if err != nil {
		var myErr *mysql.MySQLError
		if errors.As(err, &myErr) && myErr.Number == 1062 {
			// 并发下唯一约束兜底：
			//  - 命中 uq_request：网络重试，返回原借用结果；
			//  - 命中 uq_active_seat：最后一席被并发对手抢走。
			if isDupKey(myErr, "uq_request") {
				if errTx := tx.Rollback(); errTx != nil {
					return nil, errTx
				}
				existing, ferr := s.findByRequest(ctx, req.DeptID, req.RequestID)
				if ferr != nil {
					return nil, ferr
				}
				return s.toBorrowResult(existing, true)
			}
			return nil, errf(CodeNoSeat, "席位竞争失败：最后一个席位已被并发申请占用")
		}
		return nil, err
	}

	// 5) 标记席位占用。
	if _, err = tx.ExecContext(ctx,
		`UPDATE seats SET status='occupied' WHERE id=? AND status='free'`, seatID); err != nil {
		return nil, err
	}

	if err = tx.Commit(); err != nil {
		// 提交时仍可能撞上 uq_active_seat（极端并发/死锁重试场景）。
		var myErr *mysql.MySQLError
		if errors.As(err, &myErr) && myErr.Number == 1062 && isDupKey(myErr, "uq_active_seat") {
			return nil, errf(CodeNoSeat, "席位竞争失败：最后一个席位已被并发申请占用")
		}
		return nil, err
	}

	co := &store.Checkout{
		Token: token, SeatID: seatID, SeatSlot: slotNo, DeptID: req.DeptID,
		Employee: req.Employee, RequestID: req.RequestID, Offline: req.Offline,
		Status: "active", IssuedAt: issuedAt, ExpiresAt: expiresAt,
	}
	return s.toBorrowResult(co, false)
}

// Return 凭签名凭证提前归还。
// 过期凭证不具备有效归还能力：席位只能由系统回收；重复归还返回幂等结果。
func (s *Service) Return(ctx context.Context, signedCred string) (*ReturnResult, error) {
	claims, err := s.signer.Parse(signedCred)
	if err != nil {
		return nil, errf(CodeBadCredential, "凭证无效：%v", err)
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var (
		status     string
		seatID     int64
		slotNo     int
		expiresAt  time.Time
		returnedAt sql.NullTime
	)
	err = tx.QueryRowContext(ctx, `
		SELECT c.status, c.seat_id, s.slot_no, c.expires_at, c.returned_at
		FROM checkouts c JOIN seats s ON s.id=c.seat_id
		WHERE c.token=? AND c.seat_id=? AND c.dept_id=?
		FOR UPDATE OF c`,
		claims.Token, claims.SeatID, claims.DeptID).
		Scan(&status, &seatID, &slotNo, &expiresAt, &returnedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errf(CodeNotFound, "凭证对应的借用记录不存在")
	}
	if err != nil {
		return nil, err
	}

	now := s.now().Truncate(time.Microsecond)
	res := &ReturnResult{Token: claims.Token, SeatID: seatID, SeatSlot: slotNo}

	switch status {
	case "returned":
		// 重复归还：幂等返回原结果，不做任何变更。
		res.Status = "returned"
		res.Already = true
		res.Reason = "该凭证已使用过，席位此前已归还"
		if returnedAt.Valid {
			t := returnedAt.Time
			res.ReturnedAt = &t
		}
		return res, nil
	case "expired":
		// 过期凭证重复归还：席位早已回收，拒绝按"归还"处理。
		res.Status = "expired"
		res.Already = true
		res.Reason = "凭证已过期，席位已由系统回收，过期凭证不能执行归还"
		return res, nil
	case "active":
		if !now.Before(expiresAt) {
			// 凭证已到期但回收器尚未扫到：本次按系统回收处理，
			// 不承认其为"有效提前归还"。
			if _, err = tx.ExecContext(ctx,
				`UPDATE checkouts SET status='expired' WHERE id IN (
				   SELECT id FROM (SELECT id FROM checkouts WHERE token=?) t)`,
				claims.Token); err != nil {
				return nil, err
			}
			if _, err = tx.ExecContext(ctx,
				`UPDATE seats SET status='free' WHERE id=?`, seatID); err != nil {
				return nil, err
			}
			if err = tx.Commit(); err != nil {
				return nil, err
			}
			res.Status = "expired"
			res.Reason = "凭证已过期，席位由系统回收，而非提前归还"
			return res, nil
		}
		// 正常的提前归还：凭证签名有效且未到期。
		if _, err = tx.ExecContext(ctx,
			`UPDATE checkouts SET status='returned', returned_at=? WHERE token=?`,
			now, claims.Token); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx,
			`UPDATE seats SET status='free' WHERE id=?`, seatID); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		res.Status = "returned"
		res.ReturnedAt = &now
		return res, nil
	default:
		return nil, errf(CodeNotFound, "未知借用状态 %q", status)
	}
}

// Heartbeat 在线占用续期（模拟与许可服务器保持连接）。
// 仅在线、未到期的活跃借用可续期；离线凭证不支持心跳。
func (s *Service) Heartbeat(ctx context.Context, signedCred string, ttlSecond int64) (*BorrowResult, error) {
	ttl := time.Duration(ttlSecond) * time.Second
	if ttl <= 0 || ttl > maxTTL {
		return nil, errf(CodeInvalidParam, "ttlSecond 非法")
	}
	claims, err := s.signer.Parse(signedCred)
	if err != nil {
		return nil, errf(CodeBadCredential, "凭证无效：%v", err)
	}
	if claims.Offline {
		return nil, errf(CodeHeartbeatDenied, "离线借出不支持在线心跳，到期前席位保持占用")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var co store.Checkout
	err = tx.QueryRowContext(ctx, `
		SELECT c.id, c.token, c.seat_id, s.slot_no, c.dept_id, c.employee,
		       c.request_id, c.offline, c.status, c.issued_at, c.expires_at
		FROM checkouts c JOIN seats s ON s.id=c.seat_id
		WHERE c.token=? FOR UPDATE OF c`, claims.Token).
		Scan(&co.ID, &co.Token, &co.SeatID, &co.SeatSlot, &co.DeptID, &co.Employee,
			&co.RequestID, &co.Offline, &co.Status, &co.IssuedAt, &co.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errf(CodeNotFound, "借用记录不存在")
	}
	if err != nil {
		return nil, err
	}
	if co.Status != "active" {
		return nil, errf(CodeExpiredReclaim, "借用已结束（%s），无法续期", co.Status)
	}
	if !s.now().Before(co.ExpiresAt) {
		return nil, errf(CodeExpiredReclaim, "凭证已过期，无法续期，请重新申请")
	}
	newExp := s.now().Truncate(time.Microsecond).Add(ttl)
	if _, err = tx.ExecContext(ctx,
		`UPDATE checkouts SET expires_at=? WHERE id=?`, newExp, co.ID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	co.ExpiresAt = newExp
	return s.toBorrowResult(&co, true)
}

// AdjustQuota 部门负责人调整额度。
// 缩减只改 quota、绝不删除席位或抹掉现存借用；新增额度时补足席位。
func (s *Service) AdjustQuota(ctx context.Context, deptID int64, newQuota int) (*store.Occupancy, error) {
	if newQuota < 0 {
		return nil, errf(CodeInvalidParam, "额度不能为负数")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var oldQuota int
	err = tx.QueryRowContext(ctx,
		`SELECT quota FROM departments WHERE id=? FOR UPDATE`, deptID).Scan(&oldQuota)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errf(CodeNotFound, "部门 %d 不存在", deptID)
	}
	if err != nil {
		return nil, err
	}

	if _, err = tx.ExecContext(ctx,
		`UPDATE departments SET quota=? WHERE id=?`, newQuota, deptID); err != nil {
		return nil, err
	}
	// 扩额：为新槽位补席位（缩减不删除任何席位，借用记录原样保留）。
	for slot := oldQuota; slot < newQuota; slot++ {
		if _, err = tx.ExecContext(ctx,
			`INSERT IGNORE INTO seats (dept_id, slot_no, status) VALUES (?,?, 'free')`,
			deptID, slot); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.Occupancy(ctx, deptID)
}

// ReapExpired 回收所有已到期的活跃席位（离线席位同样在到期时回收）。
// 返回回收条数。
func (s *Service) ReapExpired(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	now := s.now()
	// 先释放席位，再把借用标记为 expired（同一事务）。
	resSeat, err := tx.ExecContext(ctx, `
		UPDATE seats s JOIN checkouts c ON c.seat_id = s.id
		SET s.status='free'
		WHERE c.status='active' AND c.expires_at <= ?`, now)
	if err != nil {
		return 0, err
	}
	resCo, err := tx.ExecContext(ctx, `
		UPDATE checkouts SET status='expired'
		WHERE status='active' AND expires_at <= ?`, now)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	n, _ := resCo.RowsAffected()
	_ = resSeat
	return n, nil
}

// Occupancy 查询单个部门的实时占用。
func (s *Service) Occupancy(ctx context.Context, deptID int64) (*store.Occupancy, error) {
	occs, err := s.queryOccupancy(ctx, deptID)
	if err != nil {
		return nil, err
	}
	if len(occs) == 0 {
		return nil, errf(CodeNotFound, "部门 %d 不存在", deptID)
	}
	return &occs[0], nil
}

// PoolView 组装池视图（部门额度统计 + 占用来源明细）。
func (s *Service) PoolView(ctx context.Context) (*store.PoolView, error) {
	occs, err := s.queryOccupancy(ctx, 0)
	if err != nil {
		return nil, err
	}
	now := s.now()
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.token, c.dept_id, d.name, c.seat_id, s.slot_no, c.employee,
		       c.offline, c.status, c.issued_at, c.expires_at, c.returned_at,
		       (c.status='active' AND c.expires_at <= ?) AS is_expired,
		       (s.slot_no < d.quota) AS within_quota
		FROM checkouts c
		JOIN seats s ON s.id = c.seat_id
		JOIN departments d ON d.id = c.dept_id
		ORDER BY c.id DESC LIMIT 200`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	views := make([]store.CheckoutView, 0)
	for rows.Next() {
		var v store.CheckoutView
		var offline int
		var returned sql.NullTime
		if err := rows.Scan(&v.Token, &v.DeptID, &v.DeptName, &v.SeatID, &v.SeatSlot,
			&v.Employee, &offline, &v.Status, &v.IssuedAt, &v.ExpiresAt, &returned,
			&v.Expired, &v.WithinQuota); err != nil {
			return nil, err
		}
		v.Offline = offline == 1
		if returned.Valid {
			t := returned.Time
			v.ReturnedAt = &t
		}
		views = append(views, v)
	}
	return &store.PoolView{GeneratedAt: s.now(), Departments: occs, Checkouts: views}, rows.Err()
}

func (s *Service) queryOccupancy(ctx context.Context, onlyDept int64) ([]store.Occupancy, error) {
	now := s.now()
	query := `
		SELECT d.id, d.name, d.quota,
		       (SELECT COUNT(*) FROM seats ss WHERE ss.dept_id=d.id) AS total,
		       COALESCE(SUM(c.status='active'), 0),
		       COALESCE(SUM(c.status='active' AND s.slot_no < d.quota), 0),
		       COALESCE(SUM(c.status='active' AND c.offline=0), 0),
		       COALESCE(SUM(c.status='active' AND c.offline=1), 0),
		       COALESCE(SUM(c.status='active' AND c.expires_at <= ?), 0)
		FROM departments d
		LEFT JOIN checkouts c ON c.dept_id = d.id
		LEFT JOIN seats s ON s.id = c.seat_id`
	args := []any{now}
	if onlyDept > 0 {
		query += " WHERE d.id=?"
		args = append(args, onlyDept)
	}
	query += " GROUP BY d.id, d.name, d.quota ORDER BY d.id"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]store.Occupancy, 0)
	for rows.Next() {
		var o store.Occupancy
		if err := rows.Scan(&o.DeptID, &o.DeptName, &o.Quota, &o.Total,
			&o.ActiveTotal, &o.ActiveInQuota, &o.OnlineActive,
			&o.OfflineActive, &o.ExpiredActive); err != nil {
			return nil, err
		}
		// 可新申请与借出准入规则一致：现存活跃借用（含额度外遗留）占满新额度即为 0。
		o.Available = o.Quota - o.ActiveTotal
		if o.Available < 0 {
			o.Available = 0
		}
		// 现存借用（含额度外遗留席位）超过当前额度，即"缩额后超额"。
		o.Oversubscribed = o.ActiveTotal > o.Quota
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Service) findByRequest(ctx context.Context, deptID int64, requestID string) (*store.Checkout, error) {
	var co store.Checkout
	var offline int
	err := s.db.QueryRowContext(ctx, `
		SELECT c.token, c.seat_id, s.slot_no, c.dept_id, c.employee,
		       c.request_id, c.offline, c.status, c.issued_at, c.expires_at
		FROM checkouts c JOIN seats s ON s.id=c.seat_id
		WHERE c.dept_id=? AND c.request_id=?`,
		deptID, requestID).Scan(
		&co.Token, &co.SeatID, &co.SeatSlot, &co.DeptID, &co.Employee,
		&co.RequestID, &offline, &co.Status, &co.IssuedAt, &co.ExpiresAt)
	if err != nil {
		return nil, err
	}
	co.Offline = offline == 1
	return &co, nil
}

// toBorrowResult 用借用记录重新签发凭证并组装返回体。
// 重试场景下凭证按同样的 claims 确定性重签，客户端拿到的凭证依然可用。
func (s *Service) toBorrowResult(co *store.Checkout, replay bool) (*BorrowResult, error) {
	claims := credential.Claims{
		Token: co.Token, SeatID: co.SeatID, DeptID: co.DeptID,
		User: co.Employee, Offline: co.Offline,
		IssuedAt: co.IssuedAt, ExpiresAt: co.ExpiresAt,
		RequestID: co.RequestID,
	}
	signed, err := s.signer.Issue(claims)
	if err != nil {
		return nil, err
	}
	return &BorrowResult{
		Replayed: replay, Token: co.Token, Credential: signed,
		SeatID: co.SeatID, SeatSlot: co.SeatSlot, DeptID: co.DeptID,
		Employee: co.Employee, Offline: co.Offline,
		IssuedAt: co.IssuedAt, ExpiresAt: co.ExpiresAt,
	}, nil
}

// Seed 写入演示部门与席位。
func (s *Service) Seed(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	type dept struct {
		name  string
		quota int
	}
	depts := []dept{{"CAD 设计部", 2}, {"仿真分析部", 1}}
	for _, d := range depts {
		var id int64
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM departments WHERE name=?`, d.name).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			res, err := tx.ExecContext(ctx,
				`INSERT INTO departments (name, quota) VALUES (?,?)`, d.name, d.quota)
			if err != nil {
				return err
			}
			id, _ = res.LastInsertId()
		} else if err != nil {
			return err
		}
		for slot := 0; slot < d.quota; slot++ {
			if _, err := tx.ExecContext(ctx,
				`INSERT IGNORE INTO seats (dept_id, slot_no, status) VALUES (?,?, 'free')`,
				id, slot); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func isDupKey(e *mysql.MySQLError, key string) bool {
	// 消息形如：Duplicate entry '...' for key 'checkouts.uq_request'
	return strings.Contains(e.Message, key)
}

// newToken 生成 v4 UUID。
func newToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	const hex = "0123456789abcdef"
	out := make([]byte, 36)
	j := 0
	for i := 0; i < 16; i++ {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out[j] = '-'
			j++
		}
		out[j] = hex[b[i]>>4]
		out[j+1] = hex[b[i]&0x0f]
		j += 2
	}
	return string(out)
}
