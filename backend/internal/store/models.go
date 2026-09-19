package store

import (
	"database/sql"
	"time"
)

// Department 部门额度。
type Department struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Quota     int       `json:"quota"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Occupancy 是一个部门当前的占用统计。
type Occupancy struct {
	DeptID         int64  `json:"deptId"`
	DeptName       string `json:"deptName"`
	Quota          int    `json:"quota"`
	Total          int    `json:"total"`          // 现存席位数（历史扩额遗留）
	ActiveTotal    int    `json:"activeTotal"`    // 全部活跃占用（含额度外遗留席位）
	ActiveInQuota  int    `json:"activeInQuota"`  // 槽位号 < quota 的活跃占用
	OnlineActive   int    `json:"onlineActive"`   // 在线占用
	OfflineActive  int    `json:"offlineActive"`  // 离线占用（到期前不可无凭证释放）
	ExpiredActive  int    `json:"expiredActive"`  // 已到期但尚未被回收的活跃占用
	Available      int    `json:"available"`      // 新申请可用 = max(0, quota - 现存活跃总数)
	Oversubscribed bool   `json:"oversubscribed"` // 缩减额度后现存借用已超过新额度
}

// Checkout 一条借用记录。
type Checkout struct {
	ID         int64
	Token      string
	SeatID     int64
	SeatSlot   int
	DeptID     int64
	Employee   string
	RequestID  string
	Offline    bool
	Status     string
	IssuedAt   time.Time
	ExpiresAt  time.Time
	ReturnedAt sql.NullTime
}

// CheckoutView 占用来源列表行（管理员"查看在线占用和离线到期"）。
type CheckoutView struct {
	Token       string     `json:"token"`
	DeptID      int64      `json:"deptId"`
	DeptName    string     `json:"deptName"`
	SeatID      int64      `json:"seatId"`
	SeatSlot    int        `json:"seatSlot"`
	Employee    string     `json:"employee"`
	Offline     bool       `json:"offline"`
	Status      string     `json:"status"`
	IssuedAt    time.Time  `json:"issuedAt"`
	ExpiresAt   time.Time  `json:"expiresAt"`
	ReturnedAt  *time.Time `json:"returnedAt,omitempty"`
	Expired     bool       `json:"expired"`     // status=active 且已到期（等待回收）
	WithinQuota bool       `json:"withinQuota"` // 槽位是否仍在当前额度内
}

// PoolView 池视图：部门额度 + 占用明细。
type PoolView struct {
	GeneratedAt time.Time      `json:"generatedAt"`
	Cached      bool           `json:"cached"`
	Departments []Occupancy    `json:"departments"`
	Checkouts   []CheckoutView `json:"checkouts"`
}
