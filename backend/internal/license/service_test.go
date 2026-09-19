package license

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"licensepool/internal/credential"
	"licensepool/internal/store"
)

// 测试通过环境变量指定数据库管理员 DSN（不含库名），例如：
//   LP_TEST_DSN="root@tcp(127.0.0.1:3306)/?parseTime=true" go test ./...
// 未设置时跳过。测试会自建 licensepool_test 库并重建所有表。

func openTestDB(t *testing.T) (*sql.DB, *Service) {
	t.Helper()
	dsn := os.Getenv("LP_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 LP_TEST_DSN，跳过 MySQL 集成测试")
	}
	root, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("连接 MySQL 失败: %v", err)
	}
	ctx := context.Background()
	if _, err := root.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS licensepool_test"); err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	if _, err := root.ExecContext(ctx, "DROP TABLE IF EXISTS licensepool_test.checkouts, licensepool_test.seats, licensepool_test.departments"); err != nil {
		t.Fatalf("清表失败: %v", err)
	}
	root.Close()

	testDSN := insertSchema(dsn, "licensepool_test")
	db, err := sql.Open("mysql", testDSN)
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store.MustExec(db)

	svc := NewService(db, credential.NewSigner("test-secret"))
	svc.now = func() time.Time { return time.Unix(1760000000, 0) } // 固定时钟
	return db, svc
}

func insertSchema(dsn, schema string) string {
	// 在 DSN 的 "/" 后插入库名（DSN 形如 root@tcp(...)/?parseTime=true）。
	for i := 0; i < len(dsn); i++ {
		if dsn[i] == '/' {
			return dsn[:i+1] + schema + dsn[i+1:]
		}
	}
	return dsn + schema
}

// createDept 建部门并按额度准备席位，返回部门 ID。
func createDept(t *testing.T, db *sql.DB, name string, quota int) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO departments (name, quota) VALUES (?,?)`, name, quota)
	if err != nil {
		t.Fatalf("建部门失败: %v", err)
	}
	id, _ := res.LastInsertId()
	for slot := 0; slot < quota; slot++ {
		if _, err := db.Exec(
			`INSERT INTO seats (dept_id, slot_no, status) VALUES (?,?, 'free')`,
			id, slot); err != nil {
			t.Fatalf("建席位失败: %v", err)
		}
	}
	return id
}

// TestLastSeatConcurrent 是核心用例：额度为 1 时大量并发申请，只有 1 人成功。
func TestLastSeatConcurrent(t *testing.T) {
	db, svc := openTestDB(t)
	ctx := context.Background()
	dept := createDept(t, db, "concurrency", 1)

	const n = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	success, noSeat, other := 0, 0, 0
	tokens := map[string]bool{}

	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			req := BorrowRequest{
				DeptID:    dept,
				Employee:  fmt.Sprintf("engineer-%02d", i),
				RequestID: fmt.Sprintf("req-%02d", i),
				TTLSecond: 300,
			}
			res, err := svc.Borrow(ctx, req)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				success++
				tokens[res.Token] = true
			case isCode(err, CodeNoSeat):
				noSeat++
			default:
				other++
				t.Errorf("未预期的错误: %v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if success != 1 {
		t.Fatalf("最后一个席位应有 1 人成功，实际成功 %d 人", success)
	}
	if noSeat != n-1 {
		t.Fatalf("其余 %d 人应拿到 NO_SEAT，实际 %d", n-1, noSeat)
	}
	if other != 0 {
		t.Fatalf("存在 %d 个非预期错误", other)
	}
	if len(tokens) != 1 {
		t.Fatalf("被占用席位必须唯一，实际 token=%v", tokens)
	}

	occ, err := svc.Occupancy(ctx, dept)
	if err != nil {
		t.Fatal(err)
	}
	if occ.ActiveInQuota != 1 || occ.Available != 0 {
		t.Fatalf("占用统计错误: %+v", occ)
	}
}

// TestBorrowIdempotentRetry 网络重试必须返回原借用结果（含相同凭证）。
func TestBorrowIdempotentRetry(t *testing.T) {
	db, svc := openTestDB(t)
	ctx := context.Background()
	dept := createDept(t, db, "idempotent", 1)

	req := BorrowRequest{DeptID: dept, Employee: "alice", RequestID: "rid-777", TTLSecond: 300}
	first, err := svc.Borrow(ctx, req)
	if err != nil {
		t.Fatalf("首次借用失败: %v", err)
	}
	if first.Replayed {
		t.Fatal("首次借用不应是重试回放")
	}
	for i := 0; i < 2; i++ {
		retry, err := svc.Borrow(ctx, req)
		if err != nil {
			t.Fatalf("重试失败: %v", err)
		}
		if !retry.Replayed {
			t.Fatal("重试必须标记 replayed=true")
		}
		if retry.Token != first.Token || retry.Credential != first.Credential {
			t.Fatal("重试必须返回原借用结果（token/凭证一致）")
		}
		if retry.SeatID != first.SeatID {
			t.Fatal("重试不得占用第二个席位")
		}
	}
	occ, _ := svc.Occupancy(ctx, dept)
	if occ.ActiveTotal != 1 {
		t.Fatalf("重试不得产生新借用，活跃数=%d", occ.ActiveTotal)
	}
}

// TestEarlyReturnAndDuplicate 凭证有效：提前归还成功；凭证再用一次是幂等重复归还。
func TestEarlyReturnAndDuplicate(t *testing.T) {
	db, svc := openTestDB(t)
	ctx := context.Background()
	dept := createDept(t, db, "early-return", 1)

	res, err := svc.Borrow(ctx, BorrowRequest{
		DeptID: dept, Employee: "bob", RequestID: "r1", TTLSecond: 300})
	if err != nil {
		t.Fatal(err)
	}
	out, err := svc.Return(ctx, res.Credential)
	if err != nil {
		t.Fatalf("提前归还失败: %v", err)
	}
	if out.Status != "returned" || out.Already {
		t.Fatalf("首次归还结果错误: %+v", out)
	}
	occ, _ := svc.Occupancy(ctx, dept)
	if occ.ActiveTotal != 0 || occ.Available != 1 {
		t.Fatalf("归还后席位应释放: %+v", occ)
	}

	// 凭证重复归还：返回幂等结果而非再次释放。
	out2, err := svc.Return(ctx, res.Credential)
	if err != nil {
		t.Fatalf("重复归还应幂等成功: %v", err)
	}
	if out2.Status != "returned" || !out2.Already {
		t.Fatalf("重复归还结果错误: %+v", out2)
	}
}

// TestExpiredCredentialReturnCoversReuse 覆盖"过期凭证重复归还"：
// 到期 -> 系统回收 -> 持旧凭证归还（两次）均被识别为过期回收，不能释放/影响席位。
func TestExpiredCredentialReturnCoversReuse(t *testing.T) {
	db, svc := openTestDB(t)
	ctx := context.Background()
	dept := createDept(t, db, "expiry", 1)

	base := svc.now()
	res, err := svc.Borrow(ctx, BorrowRequest{
		DeptID: dept, Employee: "carol", RequestID: "r-exp", TTLSecond: 60})
	if err != nil {
		t.Fatal(err)
	}

	// 到期前：席位有效占用，新申请被拒绝。
	if _, err := svc.Borrow(ctx, BorrowRequest{
		DeptID: dept, Employee: "dave", RequestID: "r-exp-2", TTLSecond: 60}); !isCode(err, CodeNoSeat) {
		t.Fatalf("到期前最后一席应被占用，err=%v", err)
	}

	// 时间推进到过期，回收器扫过。
	svc.now = func() time.Time { return base.Add(2 * time.Minute) }
	if n, err := svc.ReapExpired(ctx); err != nil || n != 1 {
		t.Fatalf("过期回收应回收 1 条，n=%d err=%v", n, err)
	}
	occ, _ := svc.Occupancy(ctx, dept)
	if occ.ActiveTotal != 0 || occ.Available != 1 {
		t.Fatalf("过期回收后席位应可再借: %+v", occ)
	}

	// 席位已被别人新申请占用（模拟真实场景）。
	newRes, err := svc.Borrow(ctx, BorrowRequest{
		DeptID: dept, Employee: "dave", RequestID: "r-exp-3", TTLSecond: 600})
	if err != nil {
		t.Fatalf("回收后应能重新借出: %v", err)
	}

	// 第 1 次拿过期凭证归还：只能得到 expired 结论，绝不能释放新占用。
	out, err := svc.Return(ctx, res.Credential)
	if err != nil {
		t.Fatalf("过期凭证归还是幂等语义结果，不应是传输错误: %v", err)
	}
	if out.Status != "expired" || !out.Already {
		t.Fatalf("过期凭证必须返回 expired+already，实际 %+v", out)
	}
	// 第 2 次重复归还，结果一致。
	out, err = svc.Return(ctx, res.Credential)
	if err != nil || out.Status != "expired" || !out.Already {
		t.Fatalf("过期凭证重复归还结果必须稳定: %+v err=%v", out, err)
	}
	occ, _ = svc.Occupancy(ctx, dept)
	if occ.ActiveTotal != 1 {
		t.Fatalf("过期凭证归还不得影响现存借用，活跃数=%d", occ.ActiveTotal)
	}

	// 新借用者持有效凭证仍可正常提前归还。
	out, err = svc.Return(ctx, newRes.Credential)
	if err != nil || out.Status != "returned" {
		t.Fatalf("新占用者的有效凭证归还应成功: %+v err=%v", out, err)
	}
}

// TestNoReleaseWithoutValidCredential 无有效凭证不能释放席位：
// 篡改签名 / 伪造凭证一律拒绝，且不存在"管理员直接释放"通道。
func TestNoReleaseWithoutValidCredential(t *testing.T) {
	db, svc := openTestDB(t)
	ctx := context.Background()
	dept := createDept(t, db, "forged", 1)

	res, err := svc.Borrow(ctx, BorrowRequest{
		DeptID: dept, Employee: "erin", RequestID: "r-forge", TTLSecond: 300})
	if err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{
		res.Credential[:len(res.Credential)-2] + "AA", // 篡改签名
		"not-a-credential",                            // 格式错误
		"YWJj.YWJj",                                   // 内容/签名都伪造
	} {
		if _, err := svc.Return(ctx, bad); !isCode(err, CodeBadCredential) {
			t.Fatalf("伪造凭证必须被拒绝，credential=%q err=%v", bad, err)
		}
	}
	occ, _ := svc.Occupancy(ctx, dept)
	if occ.ActiveTotal != 1 {
		t.Fatalf("被拒绝的归还不得释放席位，活跃数=%d", occ.ActiveTotal)
	}
}

// TestShrinkQuotaPreservesBorrows 缩减额度不抹掉现存借用，只限制新申请；恢复后可再借。
func TestShrinkQuotaPreservesBorrows(t *testing.T) {
	db, svc := openTestDB(t)
	ctx := context.Background()
	dept := createDept(t, db, "quota-shrink", 2)

	r1, err := svc.Borrow(ctx, BorrowRequest{DeptID: dept, Employee: "u1", RequestID: "q1", TTLSecond: 300})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := svc.Borrow(ctx, BorrowRequest{DeptID: dept, Employee: "u2", RequestID: "q2", TTLSecond: 300})
	if err != nil {
		t.Fatal(err)
	}

	// 2 -> 1：现存两条借用必须原样保留（席位/记录都不删除），且新申请被限制。
	if _, err := svc.AdjustQuota(ctx, dept, 1); err != nil {
		t.Fatal(err)
	}
	occ, _ := svc.Occupancy(ctx, dept)
	if occ.ActiveTotal != 2 {
		t.Fatalf("缩额不得抹掉现存借用，活跃总数=%d", occ.ActiveTotal)
	}
	if occ.ActiveInQuota != 1 || occ.Available != 0 || !occ.Oversubscribed {
		t.Fatalf("缩额后额度内统计错误: %+v", occ)
	}
	var seatCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM seats WHERE dept_id=?`, dept).Scan(&seatCount); err != nil || seatCount != 2 {
		t.Fatalf("缩额不得删除席位，seatCount=%d err=%v", seatCount, err)
	}

	// 现存两条借用占满新额度 1（遗留借用计数），新申请一律拒绝。
	if _, err := svc.Borrow(ctx, BorrowRequest{
		DeptID: dept, Employee: "u3", RequestID: "q3", TTLSecond: 300}); !isCode(err, CodeNoSeat) {
		t.Fatalf("缩额后新申请应被拒绝，err=%v", err)
	}

	// 额度内席位的借用归还后：新申请仍被拒绝（现存活跃仍有 1 条遗留占用）。
	if _, err := svc.Return(ctx, r1.Credential); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Borrow(ctx, BorrowRequest{
		DeptID: dept, Employee: "u3", RequestID: "q4", TTLSecond: 300}); !isCode(err, CodeNoSeat) {
		t.Fatalf("只要现存活跃借用达到额度就不允许新申请，err=%v", err)
	}
	occ, _ = svc.Occupancy(ctx, dept)
	if occ.ActiveTotal != 1 || occ.ActiveInQuota != 0 || occ.Available != 0 {
		t.Fatalf("归还额度内席位后统计错误: %+v", occ)
	}

	// 遗留借用也归还后：现存活跃 0 < 额度 1，槽位 0 可被新申请使用。
	if _, err := svc.Return(ctx, r2.Credential); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Borrow(ctx, BorrowRequest{
		DeptID: dept, Employee: "u4", RequestID: "q5", TTLSecond: 300}); err != nil {
		t.Fatalf("现存借用清空后应能在额度内新申请: %v", err)
	}
	occ, _ = svc.Occupancy(ctx, dept)
	if occ.ActiveTotal != 1 || occ.ActiveInQuota != 1 {
		t.Fatalf("新申请后统计错误: %+v", occ)
	}

	// 恢复额度到 2：补充新槽位后可再借出第 2 人。
	if _, err := svc.AdjustQuota(ctx, dept, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Borrow(ctx, BorrowRequest{
		DeptID: dept, Employee: "u5", RequestID: "q6", TTLSecond: 300}); err != nil {
		t.Fatalf("恢复额度后应能借用: %v", err)
	}
}

// TestOfflineSeatAndHeartbeat 离线席位到期前持续占用，且不能用心跳续期。
func TestOfflineSeatAndHeartbeat(t *testing.T) {
	db, svc := openTestDB(t)
	ctx := context.Background()
	dept := createDept(t, db, "offline", 1)

	res, err := svc.Borrow(ctx, BorrowRequest{
		DeptID: dept, Employee: "frank", RequestID: "off1", Offline: true, TTLSecond: 300})
	if err != nil {
		t.Fatal(err)
	}
	occ, _ := svc.Occupancy(ctx, dept)
	if occ.OfflineActive != 1 || occ.OnlineActive != 0 {
		t.Fatalf("离线占用统计错误: %+v", occ)
	}
	if _, err := svc.Heartbeat(ctx, res.Credential, 300); !isCode(err, CodeHeartbeatDenied) {
		t.Fatalf("离线凭证不应支持心跳续期，err=%v", err)
	}
}

func isCode(err error, code string) bool {
	var le *Error
	if errors.As(err, &le) {
		return le.Code == code
	}
	return false
}
