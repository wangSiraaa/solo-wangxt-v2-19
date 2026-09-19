#!/usr/bin/env bash
# 端到端演示:借出 / 并发最后一席 / 网络重试 / 提前归还 / 过期凭证重复归还 / 回收 / 额度调整
set -u
BASE=${BASE:-http://127.0.0.1:8080}
J() { python3 -c "
import sys,json
d=json.load(sys.stdin)
expr=sys.argv[1]
try:
    v=eval(expr, {'d': d})
    print('' if v is None else v)
except Exception:
    print('')
" "$1" 2>/dev/null; }

echo "================ 0. 准备:建一个仅 1 席位的演示池 ================"
DEPT=$(curl -s -XPOST $BASE/api/departments -d '{"name":"演示部-'$RANDOM'","quota":5}')
DID=$(echo "$DEPT" | J "d['id']")
POOL=$(curl -s -XPOST $BASE/api/pools -d "{\"department_id\":$DID,\"product\":\"DemoCAD\",\"version\":\"X\",\"seats\":1}")
PID=$(echo "$POOL" | J "d['id']")
echo "部门=$DID 池=$PID (1 席位)"

echo; echo "================ 1. 并发申请最后一个席位(8 个请求) ================"
TMP=$(mktemp -d)
for i in $(seq 1 8); do
  curl -s -XPOST $BASE/api/checkouts -d "{\"pool_id\":$PID,\"employee\":\"eng$i\",\"host\":\"pc$i\",\"mode\":\"OFFLINE\",\"ttl_seconds\":3600,\"idempotency_key\":\"race-$i-$RANDOM\"}" > $TMP/r$i &
done
wait
OK=0; FAIL=0
for i in $(seq 1 8); do
  if grep -q '"checkout"' $TMP/r$i; then OK=$((OK+1)); else FAIL=$((FAIL+1)); fi
done
echo "成功人数=$OK (必须为 1),被拒人数=$FAIL (必须为 7)"
[ "$OK" = "1" ] || { echo "!! 并发约束失败"; exit 1; }

echo; echo "================ 2. 网络重试返回原借用结果 ================"
# 先找到并发赢家,用其凭证提前归还,把唯一席位腾出来
WINNER_FILE=$(grep -l '"checkout"' $TMP/r*)
WIN_CRED=$(J "d['credential']" < "$WINNER_FILE")
curl -s -XPOST $BASE/api/returns -d "{\"credential\":\"$WIN_CRED\"}" >/dev/null
echo "并发赢家席位已提前归还"

KEY="retry-$RANDOM"
R1=$(curl -s -XPOST $BASE/api/checkouts -d "{\"pool_id\":$PID,\"employee\":\"retryer\",\"host\":\"pcx\",\"mode\":\"OFFLINE\",\"ttl_seconds\":3600,\"idempotency_key\":\"$KEY\"}")
ID1=$(echo "$R1" | J "d['checkout']['id']")
echo "首次借用ID=$ID1"
# 用同一个幂等键重放(此时席位已被该借用占用,若幂等失效就会报席位不足)
R2=$(curl -s -XPOST $BASE/api/checkouts -d "{\"pool_id\":$PID,\"employee\":\"retryer\",\"host\":\"pcx\",\"mode\":\"OFFLINE\",\"ttl_seconds\":3600,\"idempotency_key\":\"$KEY\"}")
ID2=$(echo "$R2" | J "d['checkout']['id']")
REPLAY=$(echo "$R2" | J "d['replayed']")
CRED=$(echo "$R2" | J "d['credential']")
echo "重试借用ID=$ID2, replayed=$REPLAY"
[ "$ID1" = "$ID2" ] && [ "$REPLAY" = "True" ] && echo "=> 幂等成立,重试返回原结果,未二次扣减" || { echo "!! 幂等失败: $R2"; exit 1; }

echo; echo "================ 3. 提前归还(有效签名凭证) ================"
RET=$(curl -s -XPOST $BASE/api/returns -d "{\"credential\":\"$CRED\"}")
echo "$RET" | head -c 200; echo
echo "--- 用同一凭证第二次归还(应 409 already_closed) ---"
curl -s -o /dev/null -w "HTTP %{http_code}\n" -XPOST $BASE/api/returns -d "{\"credential\":\"$CRED\"}"

echo; echo "================ 4. 篡改/伪造凭证被拒(401) ================"
FAKE="${CRED%??}AA"
curl -s -w " [HTTP %{http_code}]\n" -XPOST $BASE/api/returns -d "{\"credential\":\"$FAKE\"}"

echo; echo "================ 5. 过期凭证不能归还,重复归还也不行,只能回收 ================"
EX=$(curl -s -XPOST $BASE/api/checkouts -d "{\"pool_id\":$PID,\"employee\":\"expirer\",\"host\":\"pce\",\"mode\":\"OFFLINE\",\"ttl_seconds\":2,\"idempotency_key\":\"exp-$RANDOM\"}")
ECRED=$(echo "$EX" | J "d['credential']")
echo "借出 2s 有效凭证,等待到期…"; sleep 3
echo "--- 过期凭证归还(应 409 credential_expired,席位不释放) ---"
curl -s -w " [HTTP %{http_code}]\n" -XPOST $BASE/api/returns -d "{\"credential\":\"$ECRED\"}"
echo "--- 过期凭证重复归还(仍然 409,不释放) ---"
curl -s -w " [HTTP %{http_code}]\n" -XPOST $BASE/api/returns -d "{\"credential\":\"$ECRED\"}"
echo "--- 到期期间尝试借出(应 409 seats_exhausted,证明席位未被释放) ---"
curl -s -w " [HTTP %{http_code}]\n" -XPOST $BASE/api/checkouts -d "{\"pool_id\":$PID,\"employee\":\"late\",\"mode\":\"OFFLINE\",\"ttl_seconds\":60,\"idempotency_key\":\"late-$RANDOM\"}"
echo "--- 管理员过期回收 ---"
curl -s -XPOST $BASE/api/admin/sweep
echo
echo "--- 回收后再借(应成功) ---"
curl -s -XPOST $BASE/api/checkouts -d "{\"pool_id\":$PID,\"employee\":\"aftersweep\",\"mode\":\"OFFLINE\",\"ttl_seconds\":3600,\"idempotency_key\":\"as-$RANDOM\"}" | head -c 150; echo

echo; echo "================ 6. 额度调整:缩减不抹现存借用,只挡新申请 ================"
# 新建部门:额度 3,池 3 席;借出 2 个后缩到 2
D2=$(curl -s -XPOST $BASE/api/departments -d '{"name":"额度部-'$RANDOM'","quota":3}' | J "d['id']")
P2=$(curl -s -XPOST $BASE/api/pools -d "{\"department_id\":$D2,\"product\":\"QuotaCAD\",\"seats\":3}" | J "d['id']")
curl -s -XPOST $BASE/api/checkouts -d "{\"pool_id\":$P2,\"employee\":\"u1\",\"mode\":\"OFFLINE\",\"ttl_seconds\":3600,\"idempotency_key\":\"q1-$RANDOM\"}" >/dev/null
curl -s -XPOST $BASE/api/checkouts -d "{\"pool_id\":$P2,\"employee\":\"u2\",\"mode\":\"OFFLINE\",\"ttl_seconds\":3600,\"idempotency_key\":\"q2-$RANDOM\"}" >/dev/null
echo "借出 2 个。缩减到 2(==已借):"
curl -s -w " [HTTP %{http_code}]\n" -XPATCH $BASE/api/departments/$D2/quota -d '{"quota_total":2}' | head -c 200
echo "缩减到 1(<已借,应 409):"
curl -s -w " [HTTP %{http_code}]\n" -XPATCH $BASE/api/departments/$D2/quota -d '{"quota_total":1}'
echo "池仍有 1 空闲席位,但额度已满,新申请应 409 quota_exceeded:"
curl -s -w " [HTTP %{http_code}]\n" -XPOST $BASE/api/checkouts -d "{\"pool_id\":$P2,\"employee\":\"u3\",\"mode\":\"OFFLINE\",\"ttl_seconds\":3600,\"idempotency_key\":\"q3-$RANDOM\"}"
echo "调增回 5 后,新申请成功:"
curl -s -XPATCH $BASE/api/departments/$D2/quota -d '{"quota_total":5}' >/dev/null
curl -s -w " [HTTP %{http_code}]\n" -XPOST $BASE/api/checkouts -d "{\"pool_id\":$P2,\"employee\":\"u3\",\"mode\":\"OFFLINE\",\"ttl_seconds\":3600,\"idempotency_key\":\"q4-$RANDOM\"}" -o /dev/null

rm -rf $TMP
echo; echo "================ 全部演示通过 ================"
