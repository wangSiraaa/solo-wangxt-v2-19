#!/usr/bin/env bash
# 端到端演示：浮动许可证离线借出。
# 依赖：curl、jq，以及运行中的后端（默认 http://127.0.0.1:8080）。
set -euo pipefail
BASE=${BASE:-http://127.0.0.1:8080}/api

say() { printf '\n\033[1;36m=== %s ===\033[0m\n' "$*"; }
post() { curl -s -X POST "$BASE$1" -H 'Content-Type: application/json' -d "$2"; }
put()  { curl -s -X PUT  "$BASE$1" -H 'Content-Type: application/json' -d "$2"; }
get()  { curl -s "$BASE$1"; }

say "0. 当前池视图（应来自直查 MySQL，第二次带 cached=true 表示命中 Redis）"
get /pool | jq '{cached, depts: [.departments[] | {deptName, quota, available}]}'
sleep 0.2
get /pool | jq '{cached}'

say "1. CAD 设计部(deptId=1, 额度2) 在线借出"
RID1=$(cat /proc/sys/kernel/random/uuid)
B1=$(post /borrow "{\"deptId\":1,\"employee\":\"张三\",\"requestId\":\"$RID1\",\"offline\":false,\"ttlSecond\":300}")
echo "$B1" | jq '{replayed, seatSlot, employee, expiresAt}'
CRED1=$(echo "$B1" | jq -r .credential)

say "2. 模拟网络重试（同一个 requestId）-> 必须返回原借用结果 replayed=true"
post /borrow "{\"deptId\":1,\"employee\":\"张三\",\"requestId\":\"$RID1\",\"offline\":false,\"ttlSecond\":300}" \
  | jq '{replayed, seatSlot, sameCredential: (.credential == "'"$CRED1"'")}'

say "3. 离线借出第二个席位（短 TTL 8s，用于演示过期回收）"
RID2=$(cat /proc/sys/kernel/random/uuid)
B2=$(post /borrow "{\"deptId\":1,\"employee\":\"李四(出差离线)\",\"requestId\":\"$RID2\",\"offline\":true,\"ttlSecond\":8}")
echo "$B2" | jq '{seatSlot, offline, expiresAt}'
CRED2=$(echo "$B2" | jq -r .credential)

say "4. 额度已满：王五申请必须失败（409 NO_SEAT）"
post /borrow "{\"deptId\":1,\"employee\":\"王五\",\"requestId\":\"$(cat /proc/sys/kernel/random/uuid)\",\"ttlSecond\":300}" \
  | jq .

say "5. 20 人并发抢 1 个席位：仿真分析部(deptId=2, 额度1)，只有 1 人成功"
TMP=$(mktemp -d)
for i in $(seq 1 20); do
  rid=$(cat /proc/sys/kernel/random/uuid)
  curl -s -X POST "$BASE/borrow" -H 'Content-Type: application/json' \
    -d "{\"deptId\":2,\"employee\":\"并发员工$i\",\"requestId\":\"$rid\",\"ttlSecond\":300}" \
    -o "$TMP/r$i.json" &
done
wait
echo "成功人数: $(jq -s '[.[] | select(.credential)] | length' "$TMP"/r*.json)"
echo "被拒人数: $(jq -s '[.[] | select(.code=="NO_SEAT")] | length' "$TMP"/r*.json)"
rm -rf "$TMP"

say "6. 张三在线心跳续期（离线凭证会被拒绝，可自行换 CRED2 试）"
post /heartbeat "{\"credential\":\"$CRED1\",\"ttlSecond\":600}" | jq '{seatSlot, expiresAt}'

say "7. 张三凭有效签名凭证提前归还；重复归还 -> 幂等结果"
post /return "{\"credential\":\"$CRED1\"}" | jq '{status, already, reason}'
post /return "{\"credential\":\"$CRED1\"}" | jq '{status, already, reason}'

say "8. 篡改凭证签名必须被拒绝（403 BAD_CREDENTIAL，席位不释放）"
BAD="${CRED2%??}AA"
post /return "{\"credential\":\"$BAD\"}" | jq .

say "9. 部门负责人把 CAD 额度 2 -> 1：李四的离线借用原样保留，只限制新申请"
put /departments/1/quota '{"quota":1}' | jq '.department | {quota, activeTotal, activeInQuota, available, oversubscribed}'
post /borrow "{\"deptId\":1,\"employee\":\"王五\",\"requestId\":\"$(cat /proc/sys/kernel/random/uuid)\",\"ttlSecond\":300}" \
  | jq '{code, message}'

say "10. 等待 11 秒：离线席位到期，回收器自动回收（REAP_INTERVAL=1s）"
sleep 11
get /pool | jq '.departments[0] | {quota, activeTotal, offlineActive, expiredActive, available}'

say "11. 李四持【过期凭证】归还（覆盖重复归还两次）：只能得到 expired 结论，不能再释放席位"
post /return "{\"credential\":\"$CRED2\"}" | jq '{status, already, reason}'
post /return "{\"credential\":\"$CRED2\"}" | jq '{status, already, reason}'

say "12. 恢复额度到 2：补充新槽位，王五现在可以借用"
put /departments/1/quota '{"quota":2}' | jq '.department | {quota, total, available}'
post /borrow "{\"deptId\":1,\"employee\":\"王五\",\"requestId\":\"$(cat /proc/sys/kernel/random/uuid)\",\"ttlSecond\":300}" \
  | jq '{seatSlot, employee}'

say "13. 最终占用来源（管理员视图：在线/离线/已回收 全量记录）"
get /pool | jq '{cached, checkouts: [.checkouts[] | {employee, seatSlot, offline, status, expired}]}'
