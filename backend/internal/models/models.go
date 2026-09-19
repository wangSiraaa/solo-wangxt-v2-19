package models

import "time"

type Department struct {
	ID         int64     `json:"id" db:"id"`
	Name       string    `json:"name" db:"name"`
	QuotaTotal int       `json:"quota_total" db:"quota_total"`
	QuotaUsed  int       `json:"quota_used" db:"quota_used"`
	CreatedAt  time.Time `json:"created_at" db:"created_at"`
	UpdatedAt  time.Time `json:"updated_at" db:"updated_at"`
}

type Pool struct {
	ID             int64     `json:"id" db:"id"`
	Product        string    `json:"product" db:"product"`
	Version        string    `json:"version" db:"version"`
	DepartmentID   int64     `json:"department_id" db:"department_id"`
	DepartmentName string    `json:"department_name,omitempty"`
	SeatsTotal     int       `json:"seats_total" db:"seats_total"`
	SeatsAvailable int       `json:"seats_available" db:"seats_available"`
	SeatsOffline   int       `json:"seats_offline" db:"seats_offline"`
	CreatedAt      time.Time `json:"created_at" db:"created_at"`
	UpdatedAt      time.Time `json:"updated_at" db:"updated_at"`
}

// SeatsOnline 在线占用 = 总数 - 可用 - 离线占用
func (p Pool) SeatsOnline() int {
	return p.SeatsTotal - p.SeatsAvailable - p.SeatsOffline
}

type Checkout struct {
	ID             int64      `json:"id" db:"id"`
	PoolID         int64      `json:"pool_id" db:"pool_id"`
	DepartmentID   int64      `json:"department_id" db:"department_id"`
	Employee       string     `json:"employee" db:"employee"`
	Host           string     `json:"host" db:"host"`
	ClientIP       string     `json:"client_ip" db:"client_ip"`
	Mode           string     `json:"mode" db:"mode"`
	Status         string     `json:"status" db:"status"`
	IdempotencyKey string     `json:"-" db:"idempotency_key"`
	BorrowedAt     time.Time  `json:"borrowed_at" db:"borrowed_at"`
	ExpiresAt      time.Time  `json:"expires_at" db:"expires_at"`
	ReturnedAt     *time.Time `json:"returned_at,omitempty" db:"returned_at"`
	Product        string     `json:"product,omitempty"`
	DepartmentName string     `json:"department_name,omitempty"`
}

type PoolView struct {
	Pool
	Checkouts []Checkout `json:"checkouts"`
}

type DepartmentView struct {
	Department
	Pools []PoolView `json:"pools"`
}
