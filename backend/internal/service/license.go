package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"license/internal/models"
	"license/internal/service/credential"

	"github.com/go-sql-driver/mysql"
)

// 业务错误(向调用方暴露,用于映射 HTTP 状态码)。
var (
	ErrNotFound          = errors.New("not found")
	ErrSeatsExhausted    = errors.New("no seat available in pool")
	ErrQuotaExceeded     = errors.New("department quota exceeded")
	ErrDuplicateOnline   = errors.New("employee already holds an online seat")
	ErrBadCredential     = errors.New("invalid or unverifiable credential")
	ErrCredentialExpired = errors.New("credential expired; seat awaits reclamation")
	ErrNotActive         = errors.New("checkout already closed (duplicate return)")
	ErrNonceMismatch     = errors.New("credential nonce does not match record")
	ErrOnlineOnly        = errors.New("heartbeat is only valid for ONLINE checkouts")
	ErrShrinkBelowUsage  = errors.New("cannot shrink total quota below current active usage")
	ErrConflict          = errors.New("conflict")
)

const mysqlDupEntry = 1062

type LicenseService struct {
	db     *sql.DB
	cache  *Cache
	secret string
}

func NewLicenseService(d *sql.DB, c *Cache, secret string) *LicenseService {
	return &LicenseService{db: d, cache: c, secret: secret}
}

// ---------------------------------------------------------------- 管理操作

// CreateDepartment 建立部门及初始总额度。
func (s *LicenseService) CreateDepartment(ctx context.Context, name string, quota int) (models.Department, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO departments(name, quota_total, quota_used) VALUES (?, ?, 0)`, name, quota)
	if err != nil {
		return models.Department{}, mapDup(err, "department name exists")
	}
	id, _ := res.LastInsertId()
	s.cache.Invalidate(ctx, "overview")
	return s.GetDepartment(ctx, id)
}

// SetDepartmentQuota 调整部门总额度。
//   - 调增:随时允许,新申请立即可用;
//   - 调减:不能低于当前活跃借用数,现存借用不会被抹掉;
//     只在 quota_total 上设限,新申请必须满足 quota_used < quota_total。
func (s *LicenseService) SetDepartmentQuota(ctx context.Context, deptID int64, total int) (models.Department, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return models.Department{}, err
	}
	defer tx.Rollback()

	var used int
	if err := tx.QueryRowContext(ctx,
		`SELECT quota_used FROM departments WHERE id=? FOR UPDATE`, deptID).Scan(&used); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return models.Department{}, ErrNotFound
		}
		return models.Department{}, err
	}
	if total < used {
		return models.Department{}, fmt.Errorf("%w: used=%d requested=%d", ErrShrinkBelowUsage, used, total)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE departments SET quota_total=? WHERE id=?`, total, deptID); err != nil {
		return models.Department{}, err
	}
	if err := tx.Commit(); err != nil {
		return models.Department{}, err
	}
	s.cache.Invalidate(ctx, "overview")
	return s.GetDepartment(ctx, deptID)
}

// CreatePool 在部门下建立某产品的许可证池。池席位是软件方分配的资源,
// 不占部门"借用额度"(quota_used 只统计实际借出);缩减额度不影响已建池。
func (s *LicenseService) CreatePool(ctx context.Context, deptID int64, product, version string, seats int) (models.Pool, error) {
	if seats < 0 {
		return models.Pool{}, errors.New("seats must be >= 0")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.Pool{}, err
	}
	defer tx.Rollback()

	var exists int
	err = tx.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM departments WHERE id=? FOR UPDATE`, deptID).Scan(&exists)
	if err != nil {
		return models.Pool{}, err
	}
	if exists == 0 {
		return models.Pool{}, ErrNotFound
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO license_pools(product, version, department_id, seats_total, seats_available, seats_offline)
		 VALUES (?, ?, ?, ?, ?, 0)`, product, version, deptID, seats, seats)
	if err != nil {
		return models.Pool{}, mapDup(err, "pool for product already exists in department")
	}
	if err := tx.Commit(); err != nil {
		return models.Pool{}, err
	}
	s.cache.Invalidate(ctx, "overview")
	id, _ := res.LastInsertId()
	return s.getPool(ctx, id)
}

// SetPoolSeats 调整某产品池席位总数(软件方分配变化时)。
// 缩容不能低于当前占用(在线+离线),绝不抹掉现存借用;扩容立刻可借。
func (s *LicenseService) SetPoolSeats(ctx context.Context, poolID int64, total int) (models.Pool, error) {
	if total < 0 {
		return models.Pool{}, errors.New("seats must be >= 0")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.Pool{}, err
	}
	defer tx.Rollback()

	var curTotal, avail, offline int
	err = tx.QueryRowContext(ctx,
		`SELECT seats_total, seats_available, seats_offline
		 FROM license_pools WHERE id=? FOR UPDATE`, poolID).
		Scan(&curTotal, &avail, &offline)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Pool{}, ErrNotFound
	} else if err != nil {
		return models.Pool{}, err
	}
	occupied := curTotal - avail
	if total < occupied {
		return models.Pool{}, fmt.Errorf("%w: pool occupied=%d requested=%d",
			ErrShrinkBelowUsage, occupied, total)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE license_pools SET seats_total=?, seats_available=seats_available+? WHERE id=?`,
		total, total-curTotal, poolID); err != nil {
		return models.Pool{}, err
	}
	if err := tx.Commit(); err != nil {
		return models.Pool{}, err
	}
	s.cache.Invalidate(ctx, "overview")
	return s.getPool(ctx, poolID)
}

// ---------------------------------------------------------------- 借出

type CheckoutRequest struct {
	PoolID         int64
	Employee       string
	Host           string
	Mode           string // ONLINE / OFFLINE
	TTL            time.Duration
	IdempotencyKey string
	ClientIP       string
}

type CheckoutResult struct {
	Checkout   models.Checkout `json:"checkout"`
	Credential string          `json:"credential"`
	Replayed   bool            `json:"replayed"` // true = 命中幂等,返回的是原借用结果
}

func randNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Checkout 借用席位。
//
// 并发安全:
//  1. 先按 idempotency_key 查历史借用 —— 网络重试直接返回原结果(不二次扣减);
//  2. 新申请在事务内 SELECT ... FOR UPDATE 锁住池行与部门行,
//     最后一个席位时两个并发事务串行化,只有一个能看到 seats_available>0;
//  3. 在线"同人同池唯一"由唯一索引兜底。
func (s *LicenseService) Checkout(ctx context.Context, req CheckoutRequest) (CheckoutResult, error) {
	if req.Mode != "ONLINE" && req.Mode != "OFFLINE" {
		return CheckoutResult{}, errors.New("mode must be ONLINE or OFFLINE")
	}
	if req.IdempotencyKey == "" || req.Employee == "" || req.TTL <= 0 {
		return CheckoutResult{}, errors.New("employee, idempotency_key and positive ttl required")
	}

	// 步骤1:幂等探测(事务外,常见重试路径)
	if existing, cred, ok, err := s.findIdempotent(ctx, req.IdempotencyKey); err != nil {
		return CheckoutResult{}, err
	} else if ok {
		return CheckoutResult{Checkout: existing, Credential: cred, Replayed: true}, nil
	}

	// 死锁/锁等待/幂等唯一键竞争时重试整个事务流程
	var result CheckoutResult
	err := withRetry(func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		// 同键二次探测:两个请求都过了事务外探测时,这里直接回放原结果。
		if existing, cred, ok, err := s.findIdempotentTx(ctx, tx, req.IdempotencyKey); err != nil {
			return err
		} else if ok {
			result = CheckoutResult{Checkout: existing, Credential: cred, Replayed: true}
			return nil
		}

		var deptID int64
		var totalAvail, offline, deptTotal, deptUsed int
		err = tx.QueryRowContext(ctx, `
			SELECT p.department_id, p.seats_available, p.seats_offline,
			       d.quota_total, d.quota_used
			FROM license_pools p JOIN departments d ON d.id = p.department_id
			WHERE p.id=? FOR UPDATE`, req.PoolID).
			Scan(&deptID, &totalAvail, &offline, &deptTotal, &deptUsed)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if totalAvail <= 0 {
			return ErrSeatsExhausted
		}
		// 缩减额度只限制新申请:总额度已满即拒绝,不动现存借用。
		if deptUsed >= deptTotal {
			return ErrQuotaExceeded
		}

		now := time.Now()
		expires := now.Add(req.TTL)
		nonce := randNonce()

		res, err := tx.ExecContext(ctx, `
			INSERT INTO checkouts
			  (pool_id, department_id, employee, host, client_ip, mode, status,
			   idempotency_key, borrowed_at, expires_at, return_nonce)
			VALUES (?, ?, ?, ?, ?, ?, 'ACTIVE', ?, ?, ?, ?)`,
			req.PoolID, deptID, req.Employee, req.Host, req.ClientIP,
			req.Mode, req.IdempotencyKey, now, expires, nonce)
		if err != nil {
			return mapInsertErr(err)
		}
		id, _ := res.LastInsertId()

		if req.Mode == "OFFLINE" {
			_, err = tx.ExecContext(ctx,
				`UPDATE license_pools SET seats_available=seats_available-1,
				   seats_offline=seats_offline+1 WHERE id=?`, req.PoolID)
		} else {
			_, err = tx.ExecContext(ctx,
				`UPDATE license_pools SET seats_available=seats_available-1 WHERE id=?`, req.PoolID)
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE departments SET quota_used=quota_used+1 WHERE id=?`, deptID); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}

		co := models.Checkout{
			ID: id, PoolID: req.PoolID, DepartmentID: deptID,
			Employee: req.Employee, Host: req.Host, ClientIP: req.ClientIP,
			Mode: req.Mode, Status: "ACTIVE", IdempotencyKey: req.IdempotencyKey,
			BorrowedAt: now, ExpiresAt: expires,
		}
		cred := IssueCheckoutCredential(co, nonce, s.secret)
		result = CheckoutResult{Checkout: co, Credential: cred}
		return nil
	})
	if err != nil {
		return CheckoutResult{}, err
	}
	if !result.Replayed {
		s.cache.Invalidate(ctx, "overview")
	}
	return result, nil
}

// ---------------------------------------------------------------- 归还 / 回收

// ReturnByCredential 凭签名凭证提前归还。
//
// 凭证必须:签名有效 -> 未到期 -> 借用仍 ACTIVE -> nonce 与库中一致。
// 过期凭证(ErrCredentialExpired)或重复归还(ErrNotActive)都不会释放席位,
// 席位只能等 SweepExpired 回收。
func (s *LicenseService) ReturnByCredential(ctx context.Context, token string) (models.Checkout, error) {
	claims, err := parseCredential(token, s.secret)
	if err != nil {
		return models.Checkout{}, err
	}
	if err := VerifyCredentialExpiry(claims, time.Now()); err != nil {
		return models.Checkout{}, err
	}

	var out models.Checkout
	err = withRetry(func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		co, err := lockCheckout(ctx, tx, claims.CheckoutID)
		if err != nil {
			return err
		}
		if co.Status != "ACTIVE" {
			// 过期凭证重复归还、或正常归还后再次提交都落到这里
			return ErrNotActive
		}
		if co.ReturnNonce != claims.Nonce {
			return ErrNonceMismatch
		}
		if err := closeCheckoutTx(ctx, tx, co, "RETURNED", "EARLY"); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		co.Checkout.Status = "RETURNED"
		out = co.Checkout
		return nil
	})
	if err != nil {
		return models.Checkout{}, err
	}
	s.cache.Invalidate(ctx, "overview")
	return out, nil
}

type SweepResult struct {
	Reclaimed int               `json:"reclaimed"`
	Checkouts []models.Checkout `json:"checkouts"`
}

// Heartbeat 在线席位续租:仅对 ONLINE 且 ACTIVE 的借用延长心跳超时时间。
// 必须持有有效(签名正确、未到期)凭证;离线席位不走心跳,只能凭证归还或到期回收。
// 返回值中的新凭证带有续期后的过期时间,客户端应替换保存。
func (s *LicenseService) Heartbeat(ctx context.Context, token string, extend time.Duration) (models.Checkout, string, error) {
	claims, err := parseCredential(token, s.secret)
	if err != nil {
		return models.Checkout{}, "", err
	}
	if err := VerifyCredentialExpiry(claims, time.Now()); err != nil {
		return models.Checkout{}, "", err
	}
	var co models.Checkout
	var newCred string
	err = withRetry(func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		row, err := lockCheckout(ctx, tx, claims.CheckoutID)
		if err != nil {
			return err
		}
		if row.Status != "ACTIVE" {
			return ErrNotActive
		}
		if row.ReturnNonce != claims.Nonce {
			return ErrNonceMismatch
		}
		if row.Mode != "ONLINE" {
			return ErrOnlineOnly
		}
		newExpiry := time.Now().Add(extend)
		if _, err := tx.ExecContext(ctx,
			`UPDATE checkouts SET expires_at=? WHERE id=?`, newExpiry, row.ID); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		row.Checkout.ExpiresAt = newExpiry
		co = row.Checkout
		newCred = IssueCheckoutCredential(row.Checkout, row.ReturnNonce, s.secret)
		return nil
	})
	if err != nil {
		return models.Checkout{}, "", err
	}
	s.cache.Invalidate(ctx, "overview")
	return co, newCred, nil
}

// SweepExpired 回收所有已到期但仍 ACTIVE 的席位(含离线到期、在线心跳超时)。
// 无需归还凭证 —— 到期本身即回收授权;释放的席位立刻可借。
func (s *LicenseService) SweepExpired(ctx context.Context, now time.Time) (SweepResult, error) {
	var out SweepResult
	err := withRetry(func() error {
		out = SweepResult{}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		rows, err := tx.QueryContext(ctx, `
			SELECT id FROM checkouts
			WHERE status='ACTIVE' AND expires_at <= ?
			ORDER BY id LIMIT 1000`, now)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()

		for _, id := range ids {
			co, err := lockCheckout(ctx, tx, id)
			if err != nil {
				return err
			}
			if co.Status != "ACTIVE" {
				continue
			}
			if err := closeCheckoutTx(ctx, tx, co, "EXPIRED", "SWEEP"); err != nil {
				return err
			}
			co.Checkout.Status = "EXPIRED"
			out.Checkouts = append(out.Checkouts, co.Checkout)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		out.Reclaimed = len(out.Checkouts)
		return nil
	})
	if err != nil {
		return SweepResult{}, err
	}
	if out.Reclaimed > 0 {
		s.cache.Invalidate(ctx, "overview")
	}
	return out, nil
}

// ---------------------------------------------------------------- 查询

const overviewCacheKey = "overview:v1"

// GetOverview 管理员视图:部门 -> 许可证池 -> 占用来源。Redis 仅缓存此读结果。
func (s *LicenseService) GetOverview(ctx context.Context) ([]models.DepartmentView, error) {
	var view []models.DepartmentView
	if hit, err := s.cache.GetJSON(ctx, overviewCacheKey, &view); err == nil && hit {
		return view, nil
	}

	deptRows, err := s.db.QueryContext(ctx, `
		SELECT id, name, quota_total, quota_used, created_at, updated_at
		FROM departments ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer deptRows.Close()

	for deptRows.Next() {
		var d models.Department
		if err := deptRows.Scan(&d.ID, &d.Name, &d.QuotaTotal, &d.QuotaUsed,
			&d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		view = append(view, models.DepartmentView{Department: d})
	}

	poolRows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.product, p.version, p.department_id, d.name,
		       p.seats_total, p.seats_available, p.seats_offline,
		       p.created_at, p.updated_at
		FROM license_pools p JOIN departments d ON d.id=p.department_id
		ORDER BY p.id`)
	if err != nil {
		return nil, err
	}
	poolByDept := map[int64][]models.PoolView{}
	for poolRows.Next() {
		var pv models.PoolView
		if err := poolRows.Scan(&pv.ID, &pv.Product, &pv.Version, &pv.DepartmentID,
			&pv.DepartmentName, &pv.SeatsTotal, &pv.SeatsAvailable,
			&pv.SeatsOffline, &pv.CreatedAt, &pv.UpdatedAt); err != nil {
			poolRows.Close()
			return nil, err
		}
		poolByDept[pv.DepartmentID] = append(poolByDept[pv.DepartmentID], pv)
	}
	poolRows.Close()

	coRows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.pool_id, c.department_id, c.employee, c.host, c.client_ip,
		       c.mode, c.status, c.borrowed_at, c.expires_at, c.returned_at,
		       p.product, d.name
		FROM checkouts c
		JOIN license_pools p ON p.id=c.pool_id
		JOIN departments d ON d.id=c.department_id
		ORDER BY c.id`)
	if err != nil {
		return nil, err
	}
	coByPool := map[int64][]models.Checkout{}
	for coRows.Next() {
		var co models.Checkout
		var returned sql.NullTime
		if err := coRows.Scan(&co.ID, &co.PoolID, &co.DepartmentID, &co.Employee,
			&co.Host, &co.ClientIP, &co.Mode, &co.Status, &co.BorrowedAt,
			&co.ExpiresAt, &returned, &co.Product, &co.DepartmentName); err != nil {
			coRows.Close()
			return nil, err
		}
		if returned.Valid {
			t := returned.Time
			co.ReturnedAt = &t
		}
		coByPool[co.PoolID] = append(coByPool[co.PoolID], co)
	}
	coRows.Close()

	for i := range view {
		pools := poolByDept[view[i].ID]
		for j := range pools {
			pools[j].Checkouts = coByPool[pools[j].ID]
		}
		view[i].Pools = pools
	}
	// 缓存失败不影响请求正确性(缓存只是加速层)
	_ = s.cache.SetJSON(ctx, overviewCacheKey, view)
	return view, nil
}

func (s *LicenseService) GetDepartment(ctx context.Context, id int64) (models.Department, error) {
	var d models.Department
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, quota_total, quota_used, created_at, updated_at
		 FROM departments WHERE id=?`, id).
		Scan(&d.ID, &d.Name, &d.QuotaTotal, &d.QuotaUsed, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Department{}, ErrNotFound
	}
	return d, err
}

func (s *LicenseService) getPool(ctx context.Context, id int64) (models.Pool, error) {
	var p models.Pool
	err := s.db.QueryRowContext(ctx, `
		SELECT id, product, version, department_id, seats_total,
		       seats_available, seats_offline, created_at, updated_at
		FROM license_pools WHERE id=?`, id).
		Scan(&p.ID, &p.Product, &p.Version, &p.DepartmentID, &p.SeatsTotal,
			&p.SeatsAvailable, &p.SeatsOffline, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Pool{}, ErrNotFound
	}
	return p, err
}

// ---------------------------------------------------------------- 内部辅助

// checkoutRow 是 checkouts 表的完整行(含不对外暴露字段)。
type checkoutRow struct {
	models.Checkout
	ReturnNonce string
}

func (s *LicenseService) findIdempotent(ctx context.Context, key string) (models.Checkout, string, bool, error) {
	return s.findIdempotentTx(ctx, s.db, key)
}

func (s *LicenseService) findIdempotentTx(ctx context.Context, q queryable, key string) (models.Checkout, string, bool, error) {
	row, ok, err := queryCheckoutByIdem(ctx, q, key)
	if err != nil || !ok {
		return models.Checkout{}, "", ok, err
	}
	cred := IssueCheckoutCredential(row.Checkout, row.ReturnNonce, s.secret)
	return row.Checkout, cred, true, nil
}

type queryable interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func queryCheckoutByIdem(ctx context.Context, q queryable, key string) (checkoutRow, bool, error) {
	row, ok, err := scanCheckout(q.QueryRowContext(ctx, `
		SELECT id, pool_id, department_id, employee, host, client_ip, mode, status,
		       idempotency_key, borrowed_at, expires_at, return_nonce
		FROM checkouts WHERE idempotency_key=?`, key))
	return row, ok, err
}

func lockCheckout(ctx context.Context, tx *sql.Tx, id int64) (checkoutRow, error) {
	row, ok, err := scanCheckout(tx.QueryRowContext(ctx, `
		SELECT id, pool_id, department_id, employee, host, client_ip, mode, status,
		       idempotency_key, borrowed_at, expires_at, return_nonce
		FROM checkouts WHERE id=? FOR UPDATE`, id))
	if err != nil {
		return checkoutRow{}, err
	}
	if !ok {
		return checkoutRow{}, ErrNotFound
	}
	return row, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanCheckout(r rowScanner) (checkoutRow, bool, error) {
	var row checkoutRow
	err := r.Scan(&row.ID, &row.PoolID, &row.DepartmentID, &row.Employee, &row.Host,
		&row.ClientIP, &row.Mode, &row.Status, &row.IdempotencyKey,
		&row.BorrowedAt, &row.ExpiresAt, &row.ReturnNonce)
	if errors.Is(err, sql.ErrNoRows) {
		return checkoutRow{}, false, nil
	}
	if err != nil {
		return checkoutRow{}, false, err
	}
	return row, true, nil
}

// closeCheckoutTx 结束一个 ACTIVE 借用:更新状态、计数与审计表。
// 调用方必须已 FOR UPDATE 锁住 checkout 对应池/部门顺序一致的行。
func closeCheckoutTx(ctx context.Context, tx *sql.Tx, row checkoutRow, status, kind string) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE checkouts SET status=?, returned_at=? WHERE id=? AND status='ACTIVE'`,
		status, time.Now(), row.ID); err != nil {
		return err
	}
	// return_events 唯一约束保证同一借用只记一次(幂等兜底)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO return_events(checkout_id, kind) VALUES (?, ?)`, row.ID, kind); err != nil {
		return mapInsertErr(err)
	}
	if row.Mode == "OFFLINE" {
		if _, err := tx.ExecContext(ctx,
			`UPDATE license_pools SET seats_available=seats_available+1,
			    seats_offline=seats_offline-1 WHERE id=?`, row.PoolID); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx,
			`UPDATE license_pools SET seats_available=seats_available+1 WHERE id=?`, row.PoolID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE departments SET quota_used=quota_used-1 WHERE id=? AND quota_used>0`,
		row.DepartmentID); err != nil {
		return err
	}
	return nil
}

// withRetry 对 InnoDB 死锁(1213)、锁等待超时(1205)以及幂等唯一键竞争做有限重试。
func withRetry(fn func() error) error {
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		err = fn()
		if err == nil {
			return nil
		}
		var me *mysql.MySQLError
		retryable := errors.Is(err, errRetryIdempotency)
		if errors.As(err, &me) && (me.Number == 1213 || me.Number == 1205) {
			retryable = true
		}
		if retryable {
			// 指数退避 + 抖动,降低并发事务再次撞锁的概率
			base := time.Duration(5*(1<<attempt)) * time.Millisecond
			time.Sleep(base + time.Duration(attempt)*time.Millisecond)
			continue
		}
		return err
	}
	return err
}

func mapInsertErr(err error) error {
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == mysqlDupEntry {
		msg := me.Message
		switch {
		case strings.Contains(msg, "uq_checkout_idem"):
			// 唯一键竞争:外层会通过幂等查询回放,标记为可重试
			return errRetryIdempotency
		case strings.Contains(msg, "uq_active_online"):
			return ErrDuplicateOnline
		}
		return err
	}
	return err
}

var errRetryIdempotency = errors.New("idempotency race, retry lookup")

func mapDup(err error, msg string) error {
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == mysqlDupEntry {
		return fmt.Errorf("%w: %s", ErrConflict, msg)
	}
	return err
}

// IssueCheckoutCredential 用借用记录与一次性 nonce 签发本地模拟凭证。
func IssueCheckoutCredential(co models.Checkout, nonce, secret string) string {
	return credential.Issue(credential.Claims{
		CheckoutID: co.ID,
		PoolID:     co.PoolID,
		Employee:   co.Employee,
		Mode:       co.Mode,
		Nonce:      nonce,
		IssuedAt:   co.BorrowedAt.Unix(),
		ExpiresAt:  co.ExpiresAt.Unix(),
	}, secret)
}

func parseCredential(token, secret string) (*credential.Claims, error) {
	claims, err := credential.Parse(token, secret)
	if err != nil {
		if errors.Is(err, credential.ErrBadSignature) ||
			errors.Is(err, credential.ErrMalformed) {
			return nil, ErrBadCredential
		}
		return nil, err
	}
	return claims, nil
}

// VerifyCredentialExpiry 把凭证层过期错误翻译成业务错误。
func VerifyCredentialExpiry(c *credential.Claims, now time.Time) error {
	if err := credential.VerifyExpiry(c, now); errors.Is(err, credential.ErrExpired) {
		return ErrCredentialExpired
	} else if err != nil {
		return err
	}
	return nil
}
