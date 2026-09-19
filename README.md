# 浮动许可证离线借出管理

工程软件浮动许可证(Floating License)管理系统:员工从部门额度中借用席位(在线占用 /
离线借出),管理员查看占用来源与离线到期,部门负责人调整额度与池席位。

- **前端**:React + Vite,展示许可证池、在线/离线占用来源、本机签名凭证
- **后端**:Go + Gin,提供借/还/心跳/回收/额度调整接口
- **MySQL**:InnoDB 事务(`SELECT ... FOR UPDATE`)+ 唯一约束控制席位
- **Redis**:仅缓存只读的总览查询(10s TTL,写后主动失效),不参与一致性决策
- **凭证**:HMAC-SHA256 签名的本地模拟凭证 `LIC1.<payload>.<sig>`,员工本机保存

## 关键业务规则与实现位置

| 规则 | 实现 |
| --- | --- |
| 最后一个席位并发申请只有一人成功 | 事务内对池行/部门行 `FOR UPDATE` 加锁后再判断 `seats_available`,见 `Checkout` |
| 网络重试返回原借用结果 | `idempotency_key` 唯一约束 + 事务前后两次幂等探测,命中返回原凭证且 `replayed=true` |
| 在线同人同池只能一个活跃席位 | 生成列 + 唯一索引 `uq_active_online`(MySQL 部分唯一索引写法) |
| 离线席位到期前无有效凭证不能释放 | 归还必须签名正确且未到期;过期凭证返回 `credential_expired`,席位保留 |
| 过期席位由管理员/后台任务回收 | `SweepExpired` + `return_events` 唯一约束保证只回收一次 |
| 凭证重复归还不二次释放 | 状态必须为 ACTIVE,`return_events.uq_return_checkout` 再兜底 |
| 缩减额度不抹现存借用,只限制新申请 | 缩减下限是当前 `quota_used`;新申请校验 `quota_used < quota_total` |
| Redis 仅缓存查询 | `Cache` 只用于 `GET /api/overview`,所有扣减都在 MySQL 事务内 |

凭证校验顺序(任一失败即拒绝,不触碰计数):

1. 格式与 HMAC 签名(防伪造、防篡改)
2. 未到期(`exp`)——过期凭证即使重复提交也只是再次报错,席位不释放
3. 借用仍为 ACTIVE(重复归还检测)
4. payload 中一次性 `nonce` 与库中记录一致

## 目录

```
backend/            Go 服务
  migrations/       建表 SQL(事务/唯一约束/生成列)
  internal/service/ 事务逻辑 + HMAC 凭证(+集成测试)
  internal/handler/ Gin 接口
frontend/           React 控制台
scripts/            环境、启停与端到端演示脚本
```

## 本地运行(免 root,便携工具链)

需要 Go 1.23+、MySQL/MariaDB 10.5+、Redis 6+。仓库脚本假定便携工具链位于
`tools/`(可用系统安装替代,改 `scripts/env.sh` 即可)。

```bash
# 1. 启动基础设施(首次会初始化 data/mysql)
scripts/start-infra.sh

# 2. 启动后端(自动迁移建表 + 写入演示数据 + 30s 周期过期回收)
scripts/start-server.sh

# 3. 端到端演示:并发最后一席 / 幂等重试 / 提前归还 /
#    过期凭证重复归还 / 过期回收 / 额度缩减
scripts/demo.sh
```

打开 http://127.0.0.1:8080 即 React 控制台(后端托管构建产物)。
前端开发模式:`cd frontend && npm install && npm run dev`(5173 端口,代理 /api)。

## API

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/overview` | 部门→池→占用来源总览(Redis 缓存) |
| POST | `/api/departments` | 建部门 `{name, quota}` |
| PATCH | `/api/departments/:id/quota` | 调整部门额度 `{quota_total}` |
| POST | `/api/pools` | 建许可证池 `{department_id, product, version, seats}` |
| PATCH | `/api/pools/:id/seats` | 调整池席位(不能低于占用) |
| POST | `/api/checkouts` | 借席位(必带 `idempotency_key`,重试复用) |
| POST | `/api/heartbeat` | 在线席位心跳续租(凭有效凭证,返回新凭证) |
| POST | `/api/returns` | 凭签名凭证提前归还 |
| POST | `/api/admin/sweep` | 立即回收所有到期席位 |

## 测试

```bash
RUN_DB_TESTS=1 go test -race ./...
```

覆盖:20 并发抢最后一席、幂等重试回放、过期凭证归还与重复归还、伪造/篡改凭证、
提前归还、过期回收后席位复用、额度缩减保数据/挡新申请/调增放开、在线唯一席位、
在线心跳与离线拒绝心跳。
