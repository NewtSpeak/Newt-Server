#!/usr/bin/env bash
# =============================================================================
# M3 级联端到端联调脚本（双节点跨节点互听 + NodeWant 订阅剪枝）
#
# 链路：Newt-Server（voice 级联编排：AnchorLease + cascade token 签发 + 等 EdgeUp）
#       ←mTLS gRPC→ Newt-SFU ×2（级联 mTLS 信令 tcp/1884x + 节点间 WebRTC PC）
#       ← WS/WebRTC → cmd/loadbot ×2（分别落在两个节点，跨级联互听）
#
# 验收点（docs 15 BM M3：双节点两用户互听、剪枝生效）：
#   1. 两个节点 enroll → ONLINE → 进服级节点池
#   2. user1 join 落 N1（首人 = anchor）；user2 经 RTT 上报被调度到 N2；
#      join 响应在 EdgeUp 之后才返回（docs 08 §4.3）
#   3. 跨节点互听：两个 loadbot 双方 recv > 0（RTP 经节点间级联边转发）
#   4. 剪枝：bot2 unsubscribe(user1) → N1(parent) 停止向级联边转发该 track
#      （metrics：outbound_tracks active→0 / pruned→1，边 tx 包速≈0）；
#      重订阅后恢复（active→1，tx 恢复增长）
#
# 前置：本机 PostgreSQL（docker owl-server-postgres-1）、Go 工具链、python3、psql。
# 每次运行新建独立数据库 owl_e2e_cas_<时间戳>，不触碰既有库。
# =============================================================================
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SFU_DIR="${SFU_DIR:-$(cd "$ROOT/.." && pwd)/Newt-SFU}"

PG_ADMIN_URL="${PG_ADMIN_URL:-postgres://owl:owl_dev_password@127.0.0.1:5432/owl?sslmode=disable}"
DB_NAME="owl_e2e_cas_$(date +%s)"
DB_URL="postgres://owl:owl_dev_password@127.0.0.1:5432/${DB_NAME}?sslmode=disable"

APP_PORT=18081
GRPC_PORT=19444
SFU1_WSS=18445; SFU1_UDP=13478; SFU1_CAS=18843
SFU2_WSS=18446; SFU2_UDP=13479; SFU2_CAS=18844
API="http://127.0.0.1:${APP_PORT}/api/v1"
GAPI="http://127.0.0.1:${APP_PORT}/gapi/v1"

WORK="$(mktemp -d /tmp/owl-e2e-cascade.XXXXXX)"
echo "==> 工作目录: ${WORK}（日志、密钥、二进制均在此）"

for port in "$APP_PORT" "$GRPC_PORT" "$SFU1_WSS" "$SFU2_WSS" "$SFU1_CAS" "$SFU2_CAS"; do
  if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "!! 端口 $port 已被占用（可能是残留进程），请先清理:"
    lsof -nP -iTCP:"$port" -sTCP:LISTEN
    exit 1
  fi
done

SERVER_PID=""; SFU1_PID=""; SFU2_PID=""
cleanup() {
  set +e
  [ -n "$SFU1_PID" ] && kill "$SFU1_PID" 2>/dev/null
  [ -n "$SFU2_PID" ] && kill "$SFU2_PID" 2>/dev/null
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null
  wait 2>/dev/null
  echo "==> 已停止 Server/SFU×2；数据库 ${DB_NAME} 与目录 ${WORK} 保留供排查"
}
trap cleanup EXIT

jget() { python3 -c "import sys, json; d = json.loads(sys.argv[1]); print($2)" "$1"; }
now_ms() { python3 -c 'import time; print(int(time.time()*1000))'; }

# 级联边 tx 包计数（某节点 metrics 内全部 dir="tx" 求和）
tx_packets() {
  curl -s "http://127.0.0.1:$1/metrics" \
    | awk '/^owlsfu_cascade_edge_packets_total\{[^}]*dir="tx"/ {s+=$2} END {printf "%.0f\n", s+0}'
}
# 出向轨 gauge（state=active|pruned）
out_tracks() {
  curl -s "http://127.0.0.1:$1/metrics" \
    | awk -v st="state=\"$2\"" '$0 ~ "^owlsfu_cascade_outbound_tracks\\{" && $0 ~ st {printf "%.0f\n", $2; f=1; exit} END {if (!f) print 0}'
}

# -----------------------------------------------------------------------------
# 0. 独立数据库 + 编译
# -----------------------------------------------------------------------------
echo "==> 创建数据库 $DB_NAME"
psql "$PG_ADMIN_URL" -qc "CREATE DATABASE ${DB_NAME}"

echo "==> 编译 owl-server / owl-sfu / loadbot"
(cd "$ROOT/backend" && go build -o "$WORK/owl-server" ./cmd/server)
(cd "$SFU_DIR" && go build -o "$WORK/owl-sfu" ./cmd/owl-sfu && go build -o "$WORK/loadbot" ./cmd/loadbot)

# -----------------------------------------------------------------------------
# 1. 启动 Newt-Server
# -----------------------------------------------------------------------------
echo "==> 启动 Newt-Server"
mkdir -p "$WORK/server-data"
env \
  APP_ADDRESS=":${APP_PORT}" \
  APP_ENV=development \
  DATABASE_URL="$DB_URL" \
  JWT_SECRET="dev-secret-at-least-32-characters!!" \
  DATA_DIR="$WORK/server-data" \
  SFU_GRPC_ADDRESS=":${GRPC_PORT}" \
  SFU_CONTROL_PUBLIC_ENDPOINT="127.0.0.1:${GRPC_PORT}" \
  GIN_MODE=release \
  "$WORK/owl-server" >"$WORK/server.log" 2>&1 &
SERVER_PID=$!

for i in $(seq 1 60); do
  curl -sf "http://127.0.0.1:${APP_PORT}/healthz" >/dev/null 2>&1 && break
  [ "$i" = 60 ] && { echo "Server 启动超时"; tail -30 "$WORK/server.log"; exit 1; }
  sleep 0.5
done
echo "    Server 就绪"

# -----------------------------------------------------------------------------
# 2. 账号 + Guild + 语音频道
# -----------------------------------------------------------------------------
echo "==> 注册账号与频道"
ADMIN_RESP=$(curl -sf -X POST "$API/auth/register" -H 'Content-Type: application/json' \
  -d '{"username":"cas-admin","email":"cas-admin@test.dev","password":"password-e2e-1"}')
ADMIN_TOKEN=$(jget "$ADMIN_RESP" "d['access_token']")
ADMIN_ID=$(jget "$ADMIN_RESP" "d['user']['id']")

curl -sf -X POST "$GAPI/auth/signup" -H 'Content-Type: application/json' \
  -d '{"username":"cas-user2","email":"cas-user2@test.dev","password":"password-e2e-2"}' >/dev/null
# 语音路由挂 /api/v1（后台 aud），e2e 直接提权 user2 以取得后台凭证（独立 e2e 库）。
psql "$DB_URL" -qc "UPDATE users SET system_admin = true WHERE username = 'cas-user2'"
USER2_RESP=$(curl -sf -X POST "$API/auth/login" -H 'Content-Type: application/json' \
  -d '{"identifier":"cas-user2","password":"password-e2e-2"}')
USER2_TOKEN=$(jget "$USER2_RESP" "d['access_token']")
USER2_ID=$(jget "$USER2_RESP" "d['user']['id']")

GUILD=$(curl -sf -X POST "$API/guilds" -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"e2e-cascade"}')
GID=$(jget "$GUILD" "d['id']")
CHANNEL=$(curl -sf -X POST "$API/guilds/$GID/channels" -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"cascade-room","type":"VOICE"}')
CID=$(jget "$CHANNEL" "d['id']")
psql "$DB_URL" -qc "INSERT INTO members (id, guild_id, user_id, nickname, created_at) \
  VALUES (gen_random_uuid(), '$GID', '$USER2_ID', '', now())"
echo "    admin=$ADMIN_ID user2=$USER2_ID guild=$GID channel=$CID"

# -----------------------------------------------------------------------------
# 3. 两个 SFU 节点：占位 → enroll → ONLINE → 节点池
# -----------------------------------------------------------------------------
start_sfu() { # start_sfu <序号> <wss端口> <udp端口> <级联端口>
  local idx=$1 wss=$2 udp=$3 cas=$4
  local node
  node=$(curl -sf -X POST "$API/admin/sfu/nodes" -H "Authorization: Bearer $ADMIN_TOKEN" \
    -H 'Content-Type: application/json' -d "{\"display_name\":\"cas-node-$idx\"}")
  local node_id enroll_token
  node_id=$(jget "$node" "d['node_id']")
  enroll_token=$(jget "$node" "d['enrollment_token']")
  mkdir -p "$WORK/sfu$idx-data"
  cat >"$WORK/sfu$idx-config.yaml" <<EOF
node_id: "$node_id"
enroll_token: "$enroll_token"
server_enroll_endpoint: "127.0.0.1:${GRPC_PORT}"
enroll_insecure: true
data_dir: "$WORK/sfu$idx-data"
wss_listen: ":${wss}"
no_tls: true
media_udp_port: ${udp}
public_ip: "127.0.0.1"
advertise_wss_url: "ws://127.0.0.1:${wss}/ws"
cascade_listen: "127.0.0.1:${cas}"
advertise_cascade_endpoint: "127.0.0.1:${cas}"
max_users: 100
EOF
  "$WORK/owl-sfu" --config "$WORK/sfu$idx-config.yaml" >"$WORK/sfu$idx.log" 2>&1 &
  eval "SFU${idx}_PID=$!"
  eval "NODE${idx}_ID=$node_id"
  echo "    node$idx=${node_id} (wss:$wss udp:$udp cascade:$cas)"
}

echo "==> 启动两个 SFU 节点"
start_sfu 1 "$SFU1_WSS" "$SFU1_UDP" "$SFU1_CAS"
start_sfu 2 "$SFU2_WSS" "$SFU2_UDP" "$SFU2_CAS"

echo "==> 等待两个节点 ONLINE"
for i in $(seq 1 60); do
  ONLINE=$(curl -sf "$API/admin/sfu/nodes" -H "Authorization: Bearer $ADMIN_TOKEN" \
    | python3 -c "import sys,json; nodes=json.load(sys.stdin); print(sum(1 for n in nodes if n['status']=='ONLINE'))")
  [ "$ONLINE" = 2 ] && break
  [ "$i" = 60 ] && { echo "节点未全部上线"; tail -20 "$WORK/sfu1.log" "$WORK/sfu2.log"; exit 1; }
  sleep 0.5
done
echo "    两节点 ONLINE"

for nid in "$NODE1_ID" "$NODE2_ID"; do
  curl -sf -X PATCH "$API/admin/sfu/nodes/$nid" -H "Authorization: Bearer $ADMIN_TOKEN" \
    -H 'Content-Type: application/json' -d '{"enabled_for_scheduling":true}' >/dev/null
done
curl -sf -X PUT "$API/admin/guilds/$GID/node-pool" -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"candidate_node_ids\":[\"$NODE1_ID\",\"$NODE2_ID\"],\"selected_node_ids\":[\"$NODE1_ID\",\"$NODE2_ID\"]}" >/dev/null
echo "    调度开启 + 节点池 = {node1, node2}"

# -----------------------------------------------------------------------------
# 4. 两用户分别落两个节点：user1 任意入房（首人 = anchor）；
#    user2 上报「远离 user1 节点」的 RTT 样本引导调度到另一节点（docs 10 §7）
# -----------------------------------------------------------------------------
join() { curl -sf -X POST "$API/voice/join" -H "Authorization: Bearer $1" \
  -H 'Content-Type: application/json' -d "{\"guild_id\":\"$GID\",\"channel_id\":\"$CID\"}"; }

echo "==> user1 进房（首人 = 初始 anchor，docs 08 B.3）"
J1=$(join "$ADMIN_TOKEN")
U1_NODE=$(jget "$J1" "d['node_id']")
U1_WSS=$(jget "$J1" "d['advertise_wss_url']")
T1=$(jget "$J1" "d['token']")
if [ "$U1_NODE" = "$NODE1_ID" ]; then OTHER_NODE=$NODE2_ID; else OTHER_NODE=$NODE1_ID; fi
echo "    user1 → 节点 $U1_NODE"

echo "==> user2 上报 RTT（anchor 节点 400ms / 另一节点 5ms）后进房"
curl -sf -X POST "$API/voice/rtt" -H "Authorization: Bearer $USER2_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"samples\":[{\"node_id\":\"$U1_NODE\",\"rtt_ms\":400},{\"node_id\":\"$OTHER_NODE\",\"rtt_ms\":5}]}" >/dev/null

TJ0=$(now_ms)
J2=$(join "$USER2_TOKEN")
TJ1=$(now_ms)
U2_NODE=$(jget "$J2" "d['node_id']")
U2_WSS=$(jget "$J2" "d['advertise_wss_url']")
T2=$(jget "$J2" "d['token']")
echo "    user2 → 节点 ${U2_NODE}（join 含建边+等 EdgeUp 耗时 $((TJ1 - TJ0)) ms）"
if [ "$U1_NODE" = "$U2_NODE" ]; then
  echo "!! 两用户落在同一节点，无法验证级联"; exit 1
fi

echo "    级联图（DB 权威状态）："
psql "$DB_URL" -tAc "SELECT '      lease: anchor='||anchor_node_id||' epoch='||epoch||' degraded='||degraded FROM voice_anchor_leases"
psql "$DB_URL" -tAc "SELECT '      edge:  '||parent_node_id||' -> '||child_node_id||' (epoch '||epoch||')' FROM voice_cascade_edges"

# parent（anchor）节点的 metrics 端口：outbound 剪枝在 parent 侧观测
if [ "$U1_NODE" = "$NODE1_ID" ]; then PARENT_METRICS=$SFU1_WSS; else PARENT_METRICS=$SFU2_WSS; fi

# -----------------------------------------------------------------------------
# 5. 跨节点互听 + 剪枝时间线：
#    bot1(user1@N1) 持续发声 45s；bot2(user2@N2) 连通后 12s 退订 user1、28s 重订。
#    采样 parent 节点级联 tx：互听期增长 → 剪枝期停滞 → 重订后恢复。
# -----------------------------------------------------------------------------
echo "==> 启动 loadbot（bot1 持续发声；bot2 12s 退订 user1 → 28s 重订）"
"$WORK/loadbot" --ws-url "$U1_WSS" --token "$T1" --duration 45s \
  >"$WORK/bot1.out" 2>"$WORK/bot1.log" &
BOT1=$!
"$WORK/loadbot" --ws-url "$U2_WSS" --token "$T2" --duration 45s \
  --unsubscribe-user "$ADMIN_ID" --unsubscribe-after 12s --resubscribe-after 28s \
  >"$WORK/bot2.out" 2>"$WORK/bot2.log" &
BOT2=$!
BOT_START=$(now_ms)

# 等两个 bot 均连通（loadbot 日志出现 state=connected 字样）
for i in $(seq 1 60); do
  grep -q '"state":"connected"' "$WORK/bot1.log" 2>/dev/null \
    && grep -q '"state":"connected"' "$WORK/bot2.log" 2>/dev/null && break
  [ "$i" = 60 ] && { echo "loadbot 未在 15s 内连通"; tail -5 "$WORK/bot1.log" "$WORK/bot2.log"; exit 1; }
  sleep 0.25
done
CONNECT_MS=$(( $(now_ms) - BOT_START ))
echo "    双 bot 媒体连通（${CONNECT_MS} ms）"

sleep_until() { # sleep_until <自 bot 连通起的秒数>
  local target_ms=$(( BOT_START + CONNECT_MS + $1 * 1000 ))
  local now; now=$(now_ms)
  [ "$now" -lt "$target_ms" ] && sleep "$(python3 -c "print(($target_ms - $now)/1000)")"
  return 0
}

# 阶段 A（互听期，退订前）：级联 tx 必须增长
sleep_until 5
A1=$(tx_packets "$PARENT_METRICS"); sleep 3
A2=$(tx_packets "$PARENT_METRICS")
ACT_A=$(out_tracks "$PARENT_METRICS" active)
echo "    [互听期] parent 级联 tx: $A1 → ${A2}（Δ$((A2 - A1))/3s），active_tracks=$ACT_A"
[ "$((A2 - A1))" -gt 50 ] || { echo "!! 互听期级联边无转发"; exit 1; }

# 阶段 B（剪枝期，12s 退订之后）：tx 停滞 + parent 出向轨 pruned
sleep_until 17
B1=$(tx_packets "$PARENT_METRICS"); sleep 3
B2=$(tx_packets "$PARENT_METRICS")
ACT_B=$(out_tracks "$PARENT_METRICS" active)
PRU_B=$(out_tracks "$PARENT_METRICS" pruned)
echo "    [剪枝期] parent 级联 tx: $B1 → ${B2}（Δ$((B2 - B1))/3s），active_tracks=$ACT_B pruned_tracks=$PRU_B"
[ "$((B2 - B1))" -le 10 ] || { echo "!! 退订后级联边仍在转发（剪枝未生效）"; exit 1; }
[ "$ACT_B" = 0 ] && [ "$PRU_B" -ge 1 ] || { echo "!! 剪枝 metrics 不符（active=$ACT_B pruned=${PRU_B}）"; exit 1; }

# 阶段 C（重订后，28s 之后）：tx 恢复增长 + 轨恢复 active
sleep_until 33
C1=$(tx_packets "$PARENT_METRICS"); sleep 3
C2=$(tx_packets "$PARENT_METRICS")
ACT_C=$(out_tracks "$PARENT_METRICS" active)
echo "    [重订后] parent 级联 tx: $C1 → ${C2}（Δ$((C2 - C1))/3s），active_tracks=$ACT_C"
[ "$((C2 - C1))" -gt 50 ] || { echo "!! 重订阅后级联边未恢复转发"; exit 1; }
[ "$ACT_C" = 1 ] || { echo "!! 重订阅后出向轨未恢复（active=${ACT_C}）"; exit 1; }

B1_RC=0; wait $BOT1 || B1_RC=$?
B2_RC=0; wait $BOT2 || B2_RC=$?
echo "    bot1 rc=$B1_RC stats=$(tail -1 "$WORK/bot1.out")"
echo "    bot2 rc=$B2_RC stats=$(grep -v '"event"' "$WORK/bot2.out" | tail -1)"
grep '"event"' "$WORK/bot2.out" | sed 's/^/    bot2 /'
if [ "$B1_RC" != 0 ] || [ "$B2_RC" != 0 ]; then
  echo "!! 跨节点互听失败（loadbot 未收到下行 RTP）"; exit 1
fi

echo ""
echo "=========================================="
echo "M3 级联 E2E 全部通过："
echo "  跨节点互听 PASS（user1@$U1_NODE ↔ user2@${U2_NODE}，join 建边耗时 $((TJ1 - TJ0)) ms）"
echo "  剪枝 PASS（tx Δ: 互听 $((A2 - A1)) → 剪枝 $((B2 - B1)) → 恢复 $((C2 - C1)) 包/3s）"
echo "数据库: ${DB_NAME}  日志目录: ${WORK}"
echo "=========================================="
