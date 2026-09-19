# 浮动许可证离线借出系统（License Pool）

工程软件管理员管理**可离线借出的浮动许可证**的完整演示：

- 员工从部门额度中借用席位（在线 / 离线）；
- 管理员查看在线占用、离线到期、回收与归还来源；
- 部门负责人调整额度（缩减只限制新申请，不抹掉现存借用）。

技术栈：**React 18 + Vite** 展示许可证池与占用来源；**Go 1.23 + Gin** 提供借还接口；
**MySQL 8（InnoDB 事务 + 唯一约束）** 控制席位；**Redis 7 仅缓存查询**。
前端构建产物被 `go:embed` 进单个后端二进制，部署只需要一个可执行文件。

## 目录结构

```
backend/
  cmd/server/main.go          # 入口：建表/种子、过期回收循环、静态托管
  internal/config/            # 环境变量配置
  internal/store/             # 建表 DDL、数据模型
  internal/license/           # 核心业务：借出/归还/回收/调额（含集成测试）
  internal/credential/        # HMAC 签名的"本地模拟凭证"（含单元测试）
  internal/cache/             # Redis 查询缓存（可降级）
  internal/api/               # Gin 路由与错误码->HTTP 映射
frontend/                     # React 管理台（Vite）
scripts/demo.sh               # 端到端演示脚本（curl+jq）
scripts/dev-infra.sh          # 免 root 启动便携 MySQL/Redis（本环境用）
```

## 关键业务规则与实现位置

| 需求 | 实现方式 |
| --- | --- |
| 最后一个席位并发申请只有一人成功 | 借出事务内 `SELECT … FOR UPDATE` 锁部门行串行化；再由 `checkouts.active_seat` **生成列 + UNIQUE 约束**在数据库层兜底（`internal/license/service.go` 的 `Borrow`，`internal/store/schema.go`） |
| 网络重试返回原借用结果 | 客户端带 `requestId` 幂等键；`UNIQUE(dept_id, request_id)` 去重，重试（含唯一键冲突分支）重新签发**逐字节相同**的凭证返回 `replayed:true` |
| 离线席位到期前无有效归还凭证不能释放 | 归还只认 HMAC 签名凭证：签名错/格式错 → 403；没有"管理员直接释放"通道。离线凭证禁止心跳续期，到期前持续占用 |
| 过期凭证重复归还 | 到期后席位只能由回收器/同步兜底置为 `expired` 并释放；此后无论拿旧凭证归还多少次都稳定返回 `{status:"expired", already:true}`，绝不影响新占用 |
| 缩减额度不抹掉现存借用 | 调额只 `UPDATE departments.quota`（扩额才补席位，缩减不删席位/记录）；借出准入以**全部现存活跃借用数 ≥ 新额度**拒绝新申请。遗留席位在池中标记"额度外" |
| Redis 仅缓存查询 | 只有 `GET /api/pool` 走缓存；任何借/还/续期/调额/回收后删除缓存键；Redis 宕机自动直查 MySQL，交易不受影响 |

凭证形态（模拟真实离线许可证的签名文件，演示中由员工保存在浏览器 localStorage）：

```
base64url(JSON {jti, seat, dept, user, offline, iat, exp, rid}).base64url(HMAC-SHA256)
```

## HTTP 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/pool` | 池视图：部门额度统计 + 占用来源（Redis 缓存，响应含 `cached`） |
| POST | `/api/borrow` | 借用：`{deptId, employee, requestId, offline, ttlSecond}` → 凭证 |
| POST | `/api/return` | 提前归还：`{credential}`（过期/重复归还为 200 结构化结果） |
| POST | `/api/heartbeat` | 在线占用续期（离线凭证返回 403） |
| PUT | `/api/departments/:id/quota` | 部门负责人调额：`{quota}` |
| POST | `/api/admin/reap` | 立即触发过期回收（后台默认每 2s 自动扫描） |

错误码：`NO_SEAT`→409，`BAD_CREDENTIAL`/`HEARTBEAT_DENIED`→403，
`EXPIRED_RECLAIMED`→410，`NOT_FOUND`→404，`INVALID_PARAM`→400。

## 运行

依赖：Go ≥ 1.22、Node ≥ 18、MySQL 8（需生成列/CHECK）、Redis（可选，缺失自动降级）。

```bash
# 1) 准备数据库
mysql -uroot -e "CREATE DATABASE IF NOT EXISTS licensepool CHARACTER SET utf8mb4;"

# 2) 构建前端（输出到后端 embed 目录）
cd frontend && npm install && npm run build && cd ..

# 3) 启动后端（默认 DSN 指向 127.0.0.1:3306/licensepool）
cd backend
export MYSQL_DSN="root@tcp(127.0.0.1:3306)/licensepool?parseTime=true&loc=Local&charset=utf8mb4"
export REDIS_ADDR="127.0.0.1:6379"   # 可选
go run ./cmd/server                  # 监听 :8080，自动建表并播种 2 个演示部门
```

打开 http://localhost:8080 即可使用管理台；开发模式可在 `frontend/` 跑 `npm run dev`（Vite 代理 `/api`）。

常用环境变量：`HTTP_ADDR`、`MYSQL_DSN`、`REDIS_ADDR`、`REDIS_PASSWORD`、
`HMAC_SECRET`、`CACHE_TTL`（默认 5s）、`REAP_INTERVAL`（默认 2s）、`SEED_DATA`。

## 端到端演示

```bash
scripts/demo.sh        # 需要 curl、jq，后端运行在 :8080
```

脚本依次演示：缓存命中 → 在线借出 → **同 requestId 网络重试返回原结果** →
离线借出 → 满额拒绝 → **20 人并发抢最后一席仅 1 人成功** → 心跳续期 →
提前归还与重复归还幂等 → 篡改签名被拒 → **缩额 2→1 现存借用保留且新申请被拒** →
**离线到期自动回收** → **过期凭证重复归还两次** → 恢复额度后再借 → 全量占用来源。

## 测试

```bash
cd backend
export LP_TEST_DSN="root@tcp(127.0.0.1:3306)/?parseTime=true&loc=Local&charset=utf8mb4"
go test -race ./...
```

`internal/license/service_test.go` 覆盖：

- `TestLastSeatConcurrent`：额度 1、20 并发，断言恰好 1 人成功、席位唯一；
- `TestBorrowIdempotentRetry`：重试返回同一 token/凭证/席位，不产生新借用；
- `TestEarlyReturnAndDuplicate`：提前归还释放席位，凭证复用为幂等重复归还；
- `TestExpiredCredentialReturnCoversReuse`：**过期凭证重复归还**——系统回收后旧凭证两次归还均返回 `expired`，且新占用者的席位不受影响；
- `TestNoReleaseWithoutValidCredential`：篡改/伪造凭证一律拒绝，席位不释放；
- `TestShrinkQuotaPreservesBorrows`：缩额不删席位/借用，只限制新申请；
- `TestOfflineSeatAndHeartbeat`：离线占用统计与心跳拒绝。

`internal/credential/credential_test.go` 覆盖签名往返、篡改 payload/签名、异密钥与格式非法。
