package service_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"license/internal/db"
	"license/internal/service"
	"license/internal/service/credential"
)

const dsn = "app:apppass@tcp(127.0.0.1:3306)/license_test?parseTime=true&charset=utf8mb4&loc=Local"

func TestMain(m *testing.M) {
	if os.Getenv("RUN_DB_TESTS") == "" {
		os.Exit(m.Run())
	}
	if mdb, err := db.Open(dsn); err == nil {
		if err := db.Migrate(mdb, "../../migrations"); err != nil {
			fmt.Fprintf(os.Stderr, "migrate test db: %v\n", err)
			os.Exit(1)
		}
		mdb.Close()
	}
	os.Exit(m.Run())
}

type env struct {
	svc *service.LicenseService
	d   int64 // 部门ID
	p   int64 // 池ID
}

func setup(t *testing.T) *env {
	t.Helper()
	if os.Getenv("RUN_DB_TESTS") == "" {
		t.Skip("set RUN_DB_TESTS=1 to run MySQL integration tests")
	}
	mdb, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("open mysql (create db license_test first): %v", err)
	}
	// 清空相关表保证测试可重复执行
	for _, stmt := range []string{
		"DELETE FROM return_events",
		"DELETE FROM checkouts",
		"DELETE FROM license_pools",
		"DELETE FROM departments",
	} {
		if _, err := mdb.Exec(stmt); err != nil {
			t.Fatalf("cleanup %q: %v", stmt, err)
		}
	}
	cache := service.NewCache("127.0.0.1:6379")
	cache.Invalidate(context.Background(), "overview")
	svc := service.NewLicenseService(mdb, cache, "unit-test-secret")

	dept, err := svc.CreateDepartment(context.Background(),
		fmt.Sprintf("测试部-%d", time.Now().UnixNano()), 10)
	if err != nil {
		t.Fatalf("create dept: %v", err)
	}
	pool, err := svc.CreatePool(context.Background(), dept.ID, "CAD", "v1", 1)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	return &env{svc: svc, d: dept.ID, p: pool.ID}
}

func mustCheckout(t *testing.T, e *env, employee, idem string, ttl time.Duration) service.CheckoutResult {
	t.Helper()
	res, err := e.svc.Checkout(context.Background(), service.CheckoutRequest{
		PoolID: e.p, Employee: employee, Host: "host-" + employee,
		Mode: "OFFLINE", TTL: ttl, IdempotencyKey: idem, ClientIP: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("checkout %s: %v", employee, err)
	}
	return res
}

// TestLastSeatConcurrent 最后一个席位并发申请,只能一人成功。
func TestLastSeatConcurrent(t *testing.T) {
	e := setup(t)
	const n = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	var ok, exhausted int
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := e.svc.Checkout(context.Background(), service.CheckoutRequest{
				PoolID: e.p, Employee: fmt.Sprintf("eng-%02d", i),
				Host: fmt.Sprintf("pc-%02d", i), Mode: "OFFLINE",
				TTL: time.Hour, IdempotencyKey: fmt.Sprintf("idem-last-%02d", i),
			})
			mu.Lock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, service.ErrSeatsExhausted):
				exhausted++
			default:
				t.Errorf("unexpected err: %v", err)
			}
			mu.Unlock()
		}(i)
	}
	close(start)
	wg.Wait()

	if ok != 1 {
		t.Fatalf("expected exactly 1 success, got %d (exhausted=%d)", ok, exhausted)
	}
	if exhausted != n-1 {
		t.Fatalf("expected %d seats_exhausted, got %d", n-1, exhausted)
	}
}

// TestIdempotentRetry 网络重试(相同幂等键)返回原借用结果,不二次扣减。
func TestIdempotentRetry(t *testing.T) {
	e := setup(t)
	first := mustCheckout(t, e, "alice", "retry-key-1", 2*time.Hour)
	second, err := e.svc.Checkout(context.Background(), service.CheckoutRequest{
		PoolID: e.p, Employee: "alice", Host: "host-alice",
		Mode: "OFFLINE", TTL: 2 * time.Hour, IdempotencyKey: "retry-key-1",
	})
	if err != nil {
		t.Fatalf("idempotent retry failed: %v", err)
	}
	if !second.Replayed {
		t.Fatal("retry should be flagged as replayed")
	}
	if first.Checkout.ID != second.Checkout.ID {
		t.Fatalf("retry created new checkout: %d vs %d", first.Checkout.ID, second.Checkout.ID)
	}
	if first.Credential != second.Credential {
		t.Fatal("retry returned a different credential than the original result")
	}
	// 重试后池仍然只剩 0 个可用席位(没有被扣两次)
	if avail := poolAvailable(t, e); avail != 0 {
		t.Fatalf("expected 0 available after retried checkout, got %d", avail)
	}
}

// TestExpiredCredentialCannotReturn 过期凭证不能提前归还;重复归还同样被拒。
func TestExpiredCredentialCannotReturn(t *testing.T) {
	e := setup(t)
	res := mustCheckout(t, e, "bob", "exp-key-1", 1*time.Second)

	// 等到凭证到期
	time.Sleep(1100 * time.Millisecond)

	_, err := e.svc.ReturnByCredential(context.Background(), res.Credential)
	if !errors.Is(err, service.ErrCredentialExpired) {
		t.Fatalf("expected credential_expired, got %v", err)
	}

	// 过期回收后,再用同一(已过期)凭证重复归还 —— 必须失败
	swept, err := e.svc.SweepExpired(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if swept.Reclaimed != 1 {
		t.Fatalf("expected 1 reclaimed, got %d", swept.Reclaimed)
	}
	_, err = e.svc.ReturnByCredential(context.Background(), res.Credential)
	if !errors.Is(err, service.ErrCredentialExpired) {
		t.Fatalf("duplicate return of expired credential must report credential_expired, got %v", err)
	}

	// 回收已把席位放回池中
	if avail := poolAvailable(t, e); avail != 1 {
		t.Fatalf("expected seat reclaimed, available=%d", avail)
	}
}

// TestDuplicateReturnOfValidCredential 未过期凭证第二次归还被拒。
func TestDuplicateReturnOfValidCredential(t *testing.T) {
	e := setup(t)
	res := mustCheckout(t, e, "carol", "dup-key-1", time.Hour)
	if _, err := e.svc.ReturnByCredential(context.Background(), res.Credential); err != nil {
		t.Fatalf("first return: %v", err)
	}
	_, err := e.svc.ReturnByCredential(context.Background(), res.Credential)
	if !errors.Is(err, service.ErrNotActive) {
		t.Fatalf("expected already_closed on duplicate return, got %v", err)
	}
	if avail := poolAvailable(t, e); avail != 1 {
		t.Fatalf("duplicate return must not release seat twice, available=%d", avail)
	}
}

// TestForgedCredentialRejected 篡改签名/伪造凭证无法归还。
func TestForgedCredentialRejected(t *testing.T) {
	e := setup(t)
	res := mustCheckout(t, e, "dave", "forge-key-1", time.Hour)

	tampered := res.Credential[:len(res.Credential)-2] + "AA"
	if _, err := e.svc.ReturnByCredential(context.Background(), tampered); !errors.Is(err, service.ErrBadCredential) {
		t.Fatalf("tampered credential: expected bad_credential, got %v", err)
	}

	// 用另一把密钥签发的凭证也无效
	forged := credential.Issue(credential.Claims{
		CheckoutID: res.Checkout.ID, Employee: "dave",
		Nonce: "deadbeef", ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}, "attacker-secret")
	if _, err := e.svc.ReturnByCredential(context.Background(), forged); !errors.Is(err, service.ErrBadCredential) {
		t.Fatalf("forged credential: expected bad_credential, got %v", err)
	}
}

// TestEarlyReturnFreesSeat 提前归还立即可再借。
func TestEarlyReturnFreesSeat(t *testing.T) {
	e := setup(t)
	res := mustCheckout(t, e, "erin", "early-key-1", time.Hour)
	if _, err := e.svc.ReturnByCredential(context.Background(), res.Credential); err != nil {
		t.Fatalf("early return: %v", err)
	}
	mustCheckout(t, e, "frank", "early-key-2", time.Hour) // 不应得到 seats_exhausted
}

// TestSweepReclaimsThenSeatReusable 过期回收释放席位并可重新借出。
func TestSweepReclaimsThenSeatReusable(t *testing.T) {
	e := setup(t)
	mustCheckout(t, e, "gina", "sweep-key-1", time.Second)
	time.Sleep(1100 * time.Millisecond)
	res, err := e.svc.SweepExpired(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if res.Reclaimed != 1 || res.Checkouts[0].Employee != "gina" {
		t.Fatalf("unexpected sweep result: %+v", res)
	}
	mustCheckout(t, e, "henry", "sweep-key-2", time.Hour)
}

// TestQuotaShrinkKeepsExisting 缩减额度不能抹掉现存借用,只限制新申请。
func TestQuotaShrinkKeepsExisting(t *testing.T) {
	e := setup(t)
	// 部门额度 10;池1已有1个席位。再建1席位的池2,借出 1 => quota_used=1
	pool2, err := e.svc.CreatePool(context.Background(), e.d, "CAM", "v1", 1)
	if err != nil {
		t.Fatalf("create pool2: %v", err)
	}
	mustCheckoutPool(t, e, pool2.ID, "ivan", "quota-key-1", time.Hour)

	// 缩减到 1(==used)允许,现存借用完好;缩减到 0(<used)被拒。
	if _, err := e.svc.SetDepartmentQuota(context.Background(), e.d, 1); err != nil {
		t.Fatalf("shrink to used: %v", err)
	}
	if _, err := e.svc.SetDepartmentQuota(context.Background(), e.d, 0); !errors.Is(err, service.ErrShrinkBelowUsage) {
		t.Fatalf("expected shrink_below_usage, got %v", err)
	}

	// 现存借用仍可查询,没有被抹掉
	v, err := e.svc.GetOverview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range v {
		if d.ID != e.d {
			continue
		}
		if d.QuotaUsed != 1 {
			t.Fatalf("existing borrow erased: quota_used=%d", d.QuotaUsed)
		}
		for _, p := range d.Pools {
			for _, co := range p.Checkouts {
				if co.Employee == "ivan" && co.Status == "ACTIVE" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("existing ACTIVE borrow disappeared after quota shrink")
	}

	// 池1此时有空闲席位,但部门额度已满,新申请必须被额度规则挡住
	_, err = e.svc.Checkout(context.Background(), service.CheckoutRequest{
		PoolID: e.p, Employee: "newguy", Mode: "OFFLINE", TTL: time.Hour,
		IdempotencyKey: "quota-new-1",
	})
	if !errors.Is(err, service.ErrQuotaExceeded) {
		t.Fatalf("expected quota_exceeded blocking new checkout, got %v", err)
	}
}

// TestQuotaGrowAllowsNew 调增额度后,同一新申请立即成功。
func TestQuotaGrowAllowsNew(t *testing.T) {
	e := setup(t)
	mustCheckout(t, e, "judy", "qg-1", time.Hour) // 单席位池被占,used=1
	// 建一个额外池,以便"有空闲席位但额度受限"
	pool2, err := e.svc.CreatePool(context.Background(), e.d, "CAM", "v1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetDepartmentQuota(context.Background(), e.d, 1); err != nil {
		t.Fatalf("shrink: %v", err)
	}
	_, err = e.svc.Checkout(context.Background(), service.CheckoutRequest{
		PoolID: pool2.ID, Employee: "kate", Mode: "OFFLINE", TTL: time.Hour,
		IdempotencyKey: "qg-2",
	})
	if !errors.Is(err, service.ErrQuotaExceeded) {
		t.Fatalf("expected quota_exceeded, got %v", err)
	}
	if _, err := e.svc.SetDepartmentQuota(context.Background(), e.d, 5); err != nil {
		t.Fatalf("grow: %v", err)
	}
	mustCheckoutPool(t, e, pool2.ID, "kate", "qg-3", time.Hour)
}

// TestOnlineUniqueSeat 在线模式同人同池重复借用被唯一约束拒绝。
func TestOnlineUniqueSeat(t *testing.T) {
	e := setup(t)
	// 用多席位池,避免"席位不足"先于唯一约束触发
	pool, err := e.svc.CreatePool(context.Background(), e.d, "ONLINE-PROD", "v1", 5)
	if err != nil {
		t.Fatalf("create online pool: %v", err)
	}
	_, err = e.svc.Checkout(context.Background(), service.CheckoutRequest{
		PoolID: pool.ID, Employee: "leo", Mode: "ONLINE",
		TTL: 5 * time.Minute, IdempotencyKey: "on-1",
	})
	if err != nil {
		t.Fatalf("online checkout: %v", err)
	}
	_, err = e.svc.Checkout(context.Background(), service.CheckoutRequest{
		PoolID: pool.ID, Employee: "leo", Mode: "ONLINE",
		TTL: 5 * time.Minute, IdempotencyKey: "on-2",
	})
	if !errors.Is(err, service.ErrDuplicateOnline) {
		t.Fatalf("expected duplicate_online_seat, got %v", err)
	}
	// 不同员工可以同时在线借用
	_, err = e.svc.Checkout(context.Background(), service.CheckoutRequest{
		PoolID: pool.ID, Employee: "mia", Mode: "ONLINE",
		TTL: 5 * time.Minute, IdempotencyKey: "on-3",
	})
	if err != nil {
		t.Fatalf("second employee online checkout: %v", err)
	}
}

// TestOnlineHeartbeat 在线心跳续期;离线席位不能用心跳。
func TestOnlineHeartbeat(t *testing.T) {
	e := setup(t)
	pool, err := e.svc.CreatePool(context.Background(), e.d, "HB-PROD", "v1", 2)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Checkout(context.Background(), service.CheckoutRequest{
		PoolID: pool.ID, Employee: "nick", Mode: "ONLINE",
		TTL: 5 * time.Second, IdempotencyKey: "hb-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstExpiry := res.Checkout.ExpiresAt
	time.Sleep(200 * time.Millisecond)
	co, newCred, err := e.svc.Heartbeat(context.Background(), res.Credential, time.Minute)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if !co.ExpiresAt.After(firstExpiry) {
		t.Fatal("heartbeat did not extend expiry")
	}
	if newCred == "" || newCred == res.Credential {
		t.Fatal("heartbeat should reissue a credential with the new expiry")
	}

	// 离线凭证不支持心跳
	off := mustCheckoutPool(t, e, e.p, "olivia", "hb-off", time.Hour)
	if _, _, err := e.svc.Heartbeat(context.Background(), off.Credential, time.Minute); !errors.Is(err, service.ErrOnlineOnly) {
		t.Fatalf("expected online_only, got %v", err)
	}

	// 在线席位停止心跳,超时后由回收器释放:再借一个 1s TTL 的在线席位,不续期
	short, err := e.svc.Checkout(context.Background(), service.CheckoutRequest{
		PoolID: pool.ID, Employee: "nora", Mode: "ONLINE",
		TTL: time.Second, IdempotencyKey: "hb-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = off
	time.Sleep(1100 * time.Millisecond)
	if _, _, err := e.svc.Heartbeat(context.Background(), short.Credential, time.Minute); !errors.Is(err, service.ErrCredentialExpired) {
		t.Fatalf("late heartbeat on expired cred: expected credential_expired, got %v", err)
	}
	r, err := e.svc.SweepExpired(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r.Reclaimed != 1 || r.Checkouts[0].Employee != "nora" {
		t.Fatalf("expected only nora reclaimed, got %+v", r)
	}
}

func mustCheckoutPool(t *testing.T, e *env, poolID int64, employee, idem string, ttl time.Duration) service.CheckoutResult {
	t.Helper()
	res, err := e.svc.Checkout(context.Background(), service.CheckoutRequest{
		PoolID: poolID, Employee: employee, Mode: "OFFLINE",
		TTL: ttl, IdempotencyKey: idem,
	})
	if err != nil {
		t.Fatalf("checkout %s: %v", employee, err)
	}
	return res
}

func poolAvailable(t *testing.T, e *env) int {
	t.Helper()
	v, err := e.svc.GetOverview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range v {
		for _, p := range d.Pools {
			if p.ID == e.p {
				return p.SeatsAvailable
			}
		}
	}
	t.Fatal("pool not found")
	return -1
}
