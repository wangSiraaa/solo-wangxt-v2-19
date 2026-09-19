-- 浮动许可证管理：部门 / 许可证池 / 借用记录
CREATE TABLE IF NOT EXISTS departments (
  id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  name         VARCHAR(64)  NOT NULL,
  quota_total  INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '部门总额度(可分配席位上限,全产品共享)',
  quota_used   INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '活跃借用数(含离线未到期)',
  created_at   DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at   DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uq_departments_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS license_pools (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  product         VARCHAR(64)  NOT NULL COMMENT '工程软件名',
  version         VARCHAR(32)  NOT NULL DEFAULT '',
  department_id   BIGINT UNSIGNED NOT NULL,
  seats_total     INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '该池总席位',
  seats_available INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '可借席位',
  seats_offline   INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '离线借出中席位',
  created_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uq_pool_dept_product (department_id, product),
  CONSTRAINT fk_pool_department FOREIGN KEY (department_id) REFERENCES departments(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS checkouts (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  pool_id       BIGINT UNSIGNED NOT NULL,
  department_id BIGINT UNSIGNED NOT NULL COMMENT '冗余以便快速统计部门占用',
  employee      VARCHAR(64)  NOT NULL,
  host          VARCHAR(128) NOT NULL DEFAULT '' COMMENT '占用来源:机器名',
  client_ip     VARCHAR(45)  NOT NULL DEFAULT '' COMMENT '占用来源:IP',
  mode          ENUM('ONLINE','OFFLINE') NOT NULL DEFAULT 'ONLINE',
  status        ENUM('ACTIVE','RETURNED','EXPIRED') NOT NULL DEFAULT 'ACTIVE',
  idempotency_key VARCHAR(64) NOT NULL COMMENT '客户端幂等键,网络重试复用',
  borrowed_at   DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  expires_at    DATETIME(3)  NOT NULL COMMENT '离线到期时间;在线席位为心跳超时时间',
  returned_at   DATETIME(3)  NULL,
  return_nonce  VARCHAR(64)  NOT NULL COMMENT '随借用凭证下发的归还随机数',
  -- 仅在线活跃席位参与的生成列,实现"部分唯一索引":在线同员工同池仅一个 ACTIVE
  online_active_key VARCHAR(160) GENERATED ALWAYS AS
    (CASE WHEN mode='ONLINE' AND status='ACTIVE'
          THEN CONCAT(pool_id, ':', employee) END) VIRTUAL,
  PRIMARY KEY (id),
  -- 幂等:同一客户端同一键只能产生一条借用,重试返回原结果
  UNIQUE KEY uq_checkout_idem (idempotency_key),
  UNIQUE KEY uq_active_online (online_active_key),
  KEY idx_checkout_pool_status (pool_id, status),
  KEY idx_checkout_dept_status (department_id, status),
  KEY idx_checkout_expires (status, expires_at),
  CONSTRAINT fk_checkout_pool FOREIGN KEY (pool_id) REFERENCES license_pools(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 归还审计:每次有效归还/回收留痕,同一借用只能归还一次
CREATE TABLE IF NOT EXISTS return_events (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  checkout_id   BIGINT UNSIGNED NOT NULL,
  kind          ENUM('EARLY','SWEEP') NOT NULL COMMENT '提前归还 or 过期回收',
  at            DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uq_return_checkout (checkout_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
