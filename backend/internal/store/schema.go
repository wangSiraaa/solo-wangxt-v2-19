package store

import "database/sql"

// statements 是建表脚本（按 go-sql-driver 默认不支持多语句，拆成逐条执行）。
//
// 关键并发控制：
//  1. departments 行锁 (SELECT ... FOR UPDATE) 把同部门借出串行化，
//     事务内以"现存活跃借用数 >= 额度"判断能否新申请；
//  2. checkouts.active_seat 是 MySQL 8 生成列：仅活跃借用生成 seat_id，
//     配合 UNIQUE(active_seat) 在数据库层强制"一个席位最多一条活跃借用"，
//     双保险保证最后一个席位并发申请时只有一人成功。
var statements = []string{
	`CREATE TABLE IF NOT EXISTS departments (
  id          BIGINT       NOT NULL AUTO_INCREMENT,
  name        VARCHAR(64)  NOT NULL UNIQUE,
  quota       INT          NOT NULL,
  updated_at  DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  PRIMARY KEY (id),
  CONSTRAINT chk_quota_nonneg CHECK (quota >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	`CREATE TABLE IF NOT EXISTS seats (
  id        BIGINT      NOT NULL AUTO_INCREMENT,
  dept_id   BIGINT      NOT NULL,
  slot_no   INT         NOT NULL,
  status    VARCHAR(16) NOT NULL DEFAULT 'free',
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  PRIMARY KEY (id),
  UNIQUE KEY uq_seat_slot (dept_id, slot_no),
  CONSTRAINT fk_seat_dept FOREIGN KEY (dept_id) REFERENCES departments(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	`CREATE TABLE IF NOT EXISTS checkouts (
  id              BIGINT      NOT NULL AUTO_INCREMENT,
  token           CHAR(36)    NOT NULL UNIQUE,
  seat_id         BIGINT      NOT NULL,
  dept_id         BIGINT      NOT NULL,
  employee        VARCHAR(64) NOT NULL,
  request_id      VARCHAR(80) NOT NULL,
  offline         TINYINT(1)  NOT NULL DEFAULT 0,
  status          VARCHAR(16) NOT NULL DEFAULT 'active',
  issued_at       DATETIME(6) NOT NULL,
  expires_at      DATETIME(6) NOT NULL,
  returned_at     DATETIME(6) NULL,
  active_seat     BIGINT GENERATED ALWAYS AS (CASE WHEN status='active' THEN seat_id ELSE NULL END) VIRTUAL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_request (dept_id, request_id),
  UNIQUE KEY uq_active_seat (active_seat),
  KEY ix_checkouts_status (status),
  KEY ix_checkouts_dept (dept_id),
  CONSTRAINT fk_co_seat FOREIGN KEY (seat_id) REFERENCES seats(id),
  CONSTRAINT fk_co_dept FOREIGN KEY (dept_id) REFERENCES departments(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
}

// MustExec 启动期执行建表。
func MustExec(db *sql.DB) {
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			panic(err)
		}
	}
}
