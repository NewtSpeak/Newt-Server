#!/usr/bin/env bash
# =============================================================================
# 语音频道端到端联调脚本（单节点互听 + 管理员踢人计时）
#
# 链路：Owl-Server（gRPC 控制面 sfucontrol + voice 编排 + sfubridge 桥接）
#       ←mTLS gRPC→ Owl-SFU（enroll → 控制通道 → WSS 信令 + UDPMux 媒体）
#       ← WS/WebRTC → cmd/loadbot ×2（互发互收模拟 Opus RTP）
#
# 验收点：
#   1. 节点 enroll + 心跳 → ONLINE，并纳入调度（enable + 服级节点池勾选）
#   2. 两个用户 POST /voice/join 拿 Media Token（含 sid）与 advertise_wss_url
#   3. 两个 loadbot 互听：双方 recv > 0
#   4. 管理员 POST /guilds/{gid}/voice/disconnect → SFU 会话关闭，计时（目标 P99 < 1s）
#
# 前置：本机 PostgreSQL（默认 postgres://owl:owl_dev_password@localhost:5432），
#       Go 工具链，python3；Owl-SFU 仓与本仓平级（可用 SFU_DIR 覆盖）。
# 每次运行新建独立数据库 owl_e2e_<时间戳>，不触碰既有库。
# =============================================================================
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SFU_DIR="${SFU_DIR:-$(cd "$ROOT/.." && pwd)/Owl-SFU}"

PG_ADMIN_URL="${PG_ADMIN_URL:-postgres://owl:owl_dev_password@localhost:5432/owl?sslmode=disable}"
DB_NAME="owl_e2e_$(date +%s)"
DB_URL="postgres://owl:owl_dev_password@localhost:5432/${DB_NAME}?sslmode=disable"

APP_PORT=18080
GRPC_PORT=19443
SFU_WSS_PORT=18443
SFU_UDP_PORT=13478
API="http://127.0.0.1:${APP_PORT}/api/v1"
GAPI="http://127.0.0.1:${APP_PORT}/gapi/v1"

WORK="$(mktemp -d /tmp/owl-e2e.XXXXXX)"
echo "==> 工作目录: ${WORK}（日志、密钥、二进制均在此）"

# 端口占用预检：残留进程会让 gRPC 监听失败 / enroll 打到错误实例，先行失败并提示。
for port in "$APP_PORT" "$GRPC_PORT" "$SFU_WSS_PORT"; do
  if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "!! 端口 $port 已被占用（可能是上次运行残留进程），请先清理:"
    lsof -nP -iTCP:"$port" -sTCP:LISTEN
    exit 1
  fi
done

SERVER_PID=""
SFU_PID=""
cleanup() {
  set +e
  [ -n "$SFU_PID" ] && kill "$SFU_PID" 2>/dev/null
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null
  wait 2>/dev/null
  echo "==> 已停止 Server/SFU；数据库 ${DB_NAME} 与目录 ${WORK} 保留供排查"
}
trap cleanup EXIT

now_ms() { python3 -c 'import time; print(int(time.time()*1000))'; }

# jget <json> <python 表达式，d 为解析结果>
jget() { python3 -c "import sys, json; d = json.loads(sys.argv[1]); print($2)" "$1"; }

# -----------------------------------------------------------------------------
# 0. 新建独立数据库 + 编译两仓二进制
# -----------------------------------------------------------------------------
echo "==> 创建数据库 $DB_NAME"
psql "$PG_ADMIN_URL" -qc "CREATE DATABASE ${DB_NAME}"

echo "==> 编译 owl-server / owl-sfu / loadbot"
(cd "$ROOT/backend" && go build -o "$WORK/owl-server" ./cmd/server)
(cd "$SFU_DIR" && go build -o "$WORK/owl-sfu" ./cmd/owl-sfu && go build -o "$WORK/loadbot" ./cmd/loadbot)

# -----------------------------------------------------------------------------
# 1. 启动 Owl-Server（业务 API :18080 + SFU 控制面 gRPC :19443）
# -----------------------------------------------------------------------------
echo "==> 启动 Owl-Server"
mkdir -p "$WORK/server-data"
env \
  APP_ADDRESS=":${APP_PORT}" \
  DATABASE_URL="$DB_URL" \
  JWT_SECRET="e2e-secret-0123456789abcdef0123456789abcdef" \
  DATA_DIR="$WORK/server-data" \
  SFU_GRPC_ADDRESS=":${GRPC_PORT}" \
  SFU_CONTROL_PUBLIC_ENDPOINT="127.0.0.1:${GRPC_PORT}" \
  CONTROL_ADDRESS=":18444" \
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
# 2. 账号：admin（首个注册 = 系统管理员）+ 普通用户 user2（用户端注册）
# -----------------------------------------------------------------------------
echo "==> 注册账号"
ADMIN_RESP=$(curl -sf -X POST "$API/auth/register" -H 'Content-Type: application/json' \
  -d '{"username":"e2e-admin","email":"e2e-admin@test.dev","password":"password-e2e-1"}')
ADMIN_TOKEN=$(jget "$ADMIN_RESP" "d['access_token']")
ADMIN_ID=$(jget "$ADMIN_RESP" "d['user']['id']")

curl -sf -X POST "$GAPI/auth/signup" -H 'Content-Type: application/json' \
  -d '{"username":"e2e-user2","email":"e2e-user2@test.dev","password":"password-e2e-2"}' >/dev/null
# /api/v1 登录目前仅对系统管理员开放（后台/用户端 aud 隔离）；语音路由挂在 /api/v1，
# e2e 中直接把 user2 提为系统管理员以取得后台凭证（独立 e2e 库，不影响真实数据）。
psql "$DB_URL" -qc "UPDATE users SET system_admin = true WHERE username = 'e2e-user2'"
USER2_RESP=$(curl -sf -X POST "$API/auth/login" -H 'Content-Type: application/json' \
  -d '{"identifier":"e2e-user2","password":"password-e2e-2"}')
USER2_TOKEN=$(jget "$USER2_RESP" "d['access_token']")
USER2_ID=$(jget "$USER2_RESP" "d['user']['id']")
echo "    admin=$ADMIN_ID user2=$USER2_ID"

# -----------------------------------------------------------------------------
# 3. Guild + 语音频道 + user2 入服（成员行直插独立 e2e 库）
# -----------------------------------------------------------------------------
echo "==> 创建 Guild 与语音频道"
GUILD=$(curl -sf -X POST "$API/guilds" -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"e2e-voice"}')
GID=$(jget "$GUILD" "d['id']")
CHANNEL=$(curl -sf -X POST "$API/guilds/$GID/channels" -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"e2e-room","type":"VOICE"}')
CID=$(jget "$CHANNEL" "d['id']")
psql "$DB_URL" -qc "INSERT INTO members (id, guild_id, user_id, nickname, created_at) \
  VALUES (gen_random_uuid(), '$GID', '$USER2_ID', '', now())"
echo "    guild=$GID channel=$CID"

# -----------------------------------------------------------------------------
# 4. SFU 节点占位 + enroll token → 启动 owl-sfu（dev：no_tls + enroll_insecure）
# -----------------------------------------------------------------------------
echo "==> 创建 SFU 节点占位"
NODE=$(curl -sf -X POST "$API/admin/sfu/nodes" -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' -d '{"display_name":"e2e-node-1"}')
NODE_ID=$(jget "$NODE" "d['node_id']")
ENROLL_TOKEN=$(jget "$NODE" "d['enrollment_token']")
echo "    node_id=$NODE_ID"

mkdir -p "$WORK/sfu-data"
cat >"$WORK/sfu-config.yaml" <<EOF
node_id: "$NODE_ID"
enroll_token: "$ENROLL_TOKEN"
server_enroll_endpoint: "127.0.0.1:${GRPC_PORT}"
enroll_insecure: true
data_dir: "$WORK/sfu-data"
wss_listen: ":${SFU_WSS_PORT}"
no_tls: true
media_udp_port: ${SFU_UDP_PORT}
public_ip: "127.0.0.1"
advertise_wss_url: "ws://127.0.0.1:${SFU_WSS_PORT}/ws"
max_users: 100
EOF

echo "==> 启动 owl-sfu"
"$WORK/owl-sfu" --config "$WORK/sfu-config.yaml" >"$WORK/sfu.log" 2>&1 &
SFU_PID=$!

echo "==> 等待节点 ONLINE（enroll + 控制通道注册 + 心跳）"
for i in $(seq 1 60); do
  STATUS=$(curl -sf "$API/admin/sfu/nodes" -H "Authorization: Bearer $ADMIN_TOKEN" \
    | python3 -c "import sys,json; nodes=json.load(sys.stdin); print(next((n['status'] for n in nodes if n['id']=='$NODE_ID'), 'NONE'))")
  [ "$STATUS" = "ONLINE" ] && break
  [ "$i" = 60 ] && { echo "节点未上线（status=${STATUS}）"; tail -30 "$WORK/sfu.log"; exit 1; }
  sleep 0.5
done
echo "    节点 ONLINE"

echo "==> 开启调度 + 勾选进服级节点池"
curl -sf -X PATCH "$API/admin/sfu/nodes/$NODE_ID" -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' -d '{"enabled_for_scheduling":true}' >/dev/null
curl -sf -X PUT "$API/admin/guilds/$GID/node-pool" -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"candidate_node_ids\":[\"$NODE_ID\"],\"selected_node_ids\":[\"$NODE_ID\"]}" >/dev/null

# -----------------------------------------------------------------------------
# 5. 两个用户 POST /voice/join（唯一主路径）→ Media Token + advertise_wss_url
# -----------------------------------------------------------------------------
join() { # join <bearer>
  curl -sf -X POST "$API/voice/join" -H "Authorization: Bearer $1" \
    -H 'Content-Type: application/json' -d "{\"guild_id\":\"$GID\",\"channel_id\":\"$CID\"}"
}
echo "==> 用户进房"
J1=$(join "$ADMIN_TOKEN")
J2=$(join "$USER2_TOKEN")
WSS_URL=$(jget "$J1" "d['advertise_wss_url']")
T1=$(jget "$J1" "d['token']")
T2=$(jget "$J2" "d['token']")
echo "    advertise_wss_url=$WSS_URL"
echo "    join#1 session=$(jget "$J1" "d['session_id']") caps=$(jget "$J1" "d['caps']")"
echo "    join#2 session=$(jget "$J2" "d['session_id']") caps=$(jget "$J2" "d['caps']")"

# -----------------------------------------------------------------------------
# 6. 互听验证：两个 loadbot 并发 20s，双方必须 recv > 0（退出码 0）
# -----------------------------------------------------------------------------
echo "==> 互听验证（loadbot ×2，20s）"
"$WORK/loadbot" --ws-url "$WSS_URL" --token "$T1" --duration 20s \
  >"$WORK/bot1.out" 2>"$WORK/bot1.log" &
BOT1=$!
"$WORK/loadbot" --ws-url "$WSS_URL" --token "$T2" --duration 20s \
  >"$WORK/bot2.out" 2>"$WORK/bot2.log" &
BOT2=$!
B1_RC=0; wait $BOT1 || B1_RC=$?
B2_RC=0; wait $BOT2 || B2_RC=$?
echo "    bot1 rc=$B1_RC stats=$(tail -1 "$WORK/bot1.out")"
echo "    bot2 rc=$B2_RC stats=$(tail -1 "$WORK/bot2.out")"
if [ "$B1_RC" != 0 ] || [ "$B2_RC" != 0 ]; then
  echo "!! 互听验证失败"; exit 1
fi
echo "    互听 PASS（双方均收到对端下行 RTP）"

# -----------------------------------------------------------------------------
# 7. 踢人计时：user2 重进 → 媒体连通后 admin disconnect → SFU 关闭会话（目标 <1s）
# -----------------------------------------------------------------------------
echo "==> 踢人验证"
J2=$(join "$USER2_TOKEN")
T2=$(jget "$J2" "d['token']")
"$WORK/loadbot" --ws-url "$WSS_URL" --token "$T2" --duration 60s --expect-recv=false \
  >"$WORK/bot-kick.out" 2>"$WORK/bot-kick.log" &
KBOT=$!
for i in $(seq 1 60); do
  grep -q '"state":"connected"' "$WORK/bot-kick.log" 2>/dev/null && break
  [ "$i" = 60 ] && { echo "loadbot 未在 15s 内连通"; exit 1; }
  sleep 0.25
done
sleep 1  # 稳定收发

T0=$(now_ms)
curl -sf -X POST "$API/guilds/$GID/voice/disconnect" -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' -d "{\"user_id\":\"$USER2_ID\"}" >/dev/null
T_API=$(now_ms)
while kill -0 $KBOT 2>/dev/null; do sleep 0.02; done
T1_MS=$(now_ms)
wait $KBOT 2>/dev/null || true
echo "    disconnect API 往返: $((T_API - T0)) ms"
echo "    踢出请求 → loadbot 会话关闭退出: $((T1_MS - T0)) ms（目标 P99 < 1000 ms）"
grep -q 'closed by sfu' "$WORK/bot-kick.log" && echo "    loadbot 收到 SFU closed 帧（会话被服务端关闭）"
if [ $((T1_MS - T0)) -ge 1000 ]; then echo "!! 踢人耗时超过 1s"; exit 1; fi

# -----------------------------------------------------------------------------
# 8. 死亡检出（15 BI）：kill -9 SFU → 心跳 5s×3 判死 → InternalNodeDown
#    → voice 迁移引擎为该节点在房用户建 DEATH 迁移任务（单节点无目标 → 排队重试）
# -----------------------------------------------------------------------------
echo "==> 死亡检出验证（kill -9 SFU，等待硬判死 ≤15s + 扫描周期）"
J2=$(join "$USER2_TOKEN")   # 保证节点上有在房用户（admin 也仍在房）
T_KILL=$(now_ms)
kill -9 "$SFU_PID"
SFU_PID=""

DEATH_OK=0
for i in $(seq 1 60); do
  STATUS=$(curl -sf "$API/admin/sfu/nodes" -H "Authorization: Bearer $ADMIN_TOKEN" \
    | python3 -c "import sys,json; nodes=json.load(sys.stdin); print(next((n['status'] for n in nodes if n['id']=='$NODE_ID'), 'NONE'))")
  JOBS=$(psql "$DB_URL" -tAqc "SELECT count(*) FROM voice_migration_jobs WHERE reason='DEATH' AND from_node_id='$NODE_ID'")
  if [ "$STATUS" != "ONLINE" ] && [ "${JOBS:-0}" -ge 1 ]; then DEATH_OK=1; break; fi
  sleep 0.5
done
T_DEAD=$(now_ms)
if [ "$DEATH_OK" != 1 ]; then
  echo "!! 死亡检出失败：status=$STATUS death_jobs=${JOBS:-0}"
  psql "$DB_URL" -c "SELECT id, reason, state, last_error FROM voice_migration_jobs"
  exit 1
fi
echo "    节点判死 + DEATH 迁移任务创建耗时: $((T_DEAD - T_KILL)) ms（含 5s 扫描周期，硬上限 15s+5s）"
psql "$DB_URL" -tAc "SELECT '    job: reason='||reason||' state='||state||' attempt='||attempt||' last_error='||coalesce(nullif(last_error,''),'-') FROM voice_migration_jobs WHERE reason='DEATH'" | head -5
echo "    单节点环境无迁移目标，任务按 docs 09 K.3 进入 FAILED/排队重试属预期"

echo ""
echo "=========================================="
echo "E2E 全部通过：互听 PASS + 踢人 $((T1_MS - T0)) ms + 判死 $((T_DEAD - T_KILL)) ms"
echo "数据库: ${DB_NAME}  日志目录: ${WORK}"
echo "=========================================="
