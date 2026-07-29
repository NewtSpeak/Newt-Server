#!/usr/bin/env bash
# =============================================================================
# M5 舞台 caps 衔接 + 屏幕轨端到端联调脚本（docs 11 AD.4/AF、14、15 §7/§9 BM M5）
#
# 链路：Newt-Server（stage bring-up/bring-down → InternalCapsDirty → voice caps 重算
#       → sfuctl UpdateParticipantCaps）←mTLS gRPC→ Newt-SFU ×2（caps 执行 +
#       无 cap 音频轨挂起接纳 + 级联视频转发）← WS/WebRTC → cmd/loadbot ×2
#
# 场景 A（STAGE 模式舞台 caps，跨节点）：
#   1. 频道设 STAGE：两个 AUDIENCE bot 分落两节点，双方持续推流但对端 recv≈0
#      （AUDIENCE 无 publish_audio，SFU 挂起接纳 + 包级门控）
#   2. bring-up bot1 → bot2 <1s 开始收包（15 BM M5：抱上 caps 生效 <1s）
#   3. bring-down bot1 → bot2 <1s 停止收包
#
# 场景 B（屏幕轨结构 + 跨节点级联视频冒烟，FREE 频道）：
#   4. 无 publish_screen 的 token 推视频轨 → 对端 recv_video=0（renegotiation 剥离）
#   5. screen/start（占坑审批）→ refresh-token 携带 publish_screen → 重推视频轨
#      → 对端（另一节点，经级联边）recv_video 增长；ScreenSlot RESERVED→ACTIVE
#
# 前置：本机 PostgreSQL（docker newt-server-postgres-1）、Go 工具链、python3、psql。
# 每次运行新建独立数据库 owl_e2e_stage_<时间戳>，不触碰既有库。
# =============================================================================
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SFU_DIR="${SFU_DIR:-$(cd "$ROOT/.." && pwd)/Newt-SFU}"

PG_ADMIN_URL="${PG_ADMIN_URL:-postgres://owl:owl_dev_password@127.0.0.1:5432/owl?sslmode=disable}"
DB_NAME="owl_e2e_stage_$(date +%s)"
DB_URL="postgres://owl:owl_dev_password@127.0.0.1:5432/${DB_NAME}?sslmode=disable"

APP_PORT=18083
GRPC_PORT=19448
SFU1_WSS=18449; SFU1_UDP=13482; SFU1_CAS=18847
SFU2_WSS=18450; SFU2_UDP=13483; SFU2_CAS=18848
API="http://127.0.0.1:${APP_PORT}/api/v1"
GAPI="http://127.0.0.1:${APP_PORT}/gapi/v1"
SERVER_URL="http://127.0.0.1:${APP_PORT}"

WORK="$(mktemp -d /tmp/owl-e2e-stage.XXXXXX)"
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
  pkill -P $$ 2>/dev/null
  [ -n "$SFU1_PID" ] && kill "$SFU1_PID" 2>/dev/null
  [ -n "$SFU2_PID" ] && kill "$SFU2_PID" 2>/dev/null
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null
  wait 2>/dev/null
  echo "==> 已停止 Server/SFU×2；数据库 ${DB_NAME} 与目录 ${WORK} 保留供排查"
}
trap cleanup EXIT

jget() { python3 -c "import sys, json; d = json.loads(sys.argv[1]); print($2)" "$1"; }
now_ms() { python3 -c 'import time; print(int(time.time()*1000))'; }

# 最近一条统计行里某计数键的值（无匹配回 0）。
last_stat() { # last_stat <out文件> <键>
  { grep -o "\"$2\":[0-9]*" "$1" 2>/dev/null || true; } | tail -1 | cut -d: -f2
}

# 首条 <键> 超过阈值的统计行的 at_ms（供 <1s 时序断言；无匹配输出 -1）。
first_growth_ms() { # first_growth_ms <out文件> <键> <阈值>
  python3 - "$1" "$2" "$3" <<'EOF'
import json, sys
path, key, thresh = sys.argv[1], sys.argv[2], int(sys.argv[3])
for line in open(path):
    line = line.strip()
    if not line.startswith("{"):
        continue
    try:
        d = json.loads(line)
    except json.JSONDecodeError:
        continue
    if d.get(key, 0) > thresh and "at_ms" in d:
        print(d["at_ms"]); break
else:
    print(-1)
EOF
}

wait_stat_over() { # wait_stat_over <out文件> <键> <阈值> <超时秒> <描述>
  local f=$1 key=$2 min=$3 timeout=$4 desc=$5 i v
  for i in $(seq 1 $((timeout * 4))); do
    v=$(last_stat "$f" "$key"); v=${v:-0}
    [ "$v" -gt "$min" ] && { echo "    ${desc}: ${key}=${v}"; return 0; }
    sleep 0.25
  done
  echo "!! ${desc} 超时（${key}=$(last_stat "$f" "$key")，期望 > ${min}）"
  return 1
}

user_node() { psql "$DB_URL" -tAc "SELECT node_id FROM voice_states WHERE user_id = '$1' AND channel_id IS NOT NULL"; }
wait_user_on_node() { # wait_user_on_node <user_id> <node_id> <超时秒> <描述>
  local uid=$1 nid=$2 timeout=$3 desc=$4 i
  for i in $(seq 1 $((timeout * 2))); do
    [ "$(user_node "$uid")" = "$nid" ] && { echo "    ${desc}: 已落 ${nid}"; return 0; }
    sleep 0.5
  done
  echo "!! ${desc} 超时（当前节点 $(user_node "$uid")，期望 ${nid}）"
  return 1
}

# -----------------------------------------------------------------------------
# 0. 独立数据库 + 编译
# -----------------------------------------------------------------------------
echo "==> 创建数据库 $DB_NAME"
psql "$PG_ADMIN_URL" -qc "CREATE DATABASE ${DB_NAME}"

echo "==> 编译 newt-server / newt-sfu / loadbot"
(cd "$ROOT/backend" && go build -o "$WORK/newt-server" ./cmd/server)
(cd "$SFU_DIR" && go build -o "$WORK/newt-sfu" ./cmd/newt-sfu && go build -o "$WORK/loadbot" ./cmd/loadbot)

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
  "$WORK/newt-server" >"$WORK/server.log" 2>&1 &
SERVER_PID=$!

for i in $(seq 1 60); do
  curl -sf "http://127.0.0.1:${APP_PORT}/healthz" >/dev/null 2>&1 && break
  [ "$i" = 60 ] && { echo "Server 启动超时"; tail -30 "$WORK/server.log"; exit 1; }
  sleep 0.5
done
echo "    Server 就绪"

# -----------------------------------------------------------------------------
# 2. 账号（admin + 两个 bot 用户）+ Guild + 两个语音频道（STAGE / 屏幕各一间）
# -----------------------------------------------------------------------------
echo "==> 注册账号与频道"
ADMIN_RESP=$(curl -sf -X POST "$API/auth/register" -H 'Content-Type: application/json' \
  -d '{"username":"stage-admin","email":"stage-admin@test.dev","password":"password-e2e-1"}')
ADMIN_TOKEN=$(jget "$ADMIN_RESP" "d['access_token']")
ADMIN_ID=$(jget "$ADMIN_RESP" "d['user']['id']")

BOT1_RESP=$(curl -sf -X POST "$GAPI/auth/signup" -H 'Content-Type: application/json' \
  -d '{"username":"stage-bot1","email":"stage-bot1@test.dev","password":"password-e2e-2"}')
BOT1_ID=$(jget "$BOT1_RESP" "d['user']['id']")
BOT2_RESP=$(curl -sf -X POST "$GAPI/auth/signup" -H 'Content-Type: application/json' \
  -d '{"username":"stage-bot2","email":"stage-bot2@test.dev","password":"password-e2e-2"}')
BOT2_ID=$(jget "$BOT2_RESP" "d['user']['id']")
BOT2_TOKEN=$(jget "$BOT2_RESP" "d['access_token']")

GUILD=$(curl -sf -X POST "$API/guilds" -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"e2e-stage"}')
GID=$(jget "$GUILD" "d['id']")
make_channel() {
  local resp
  resp=$(curl -sf -X POST "$API/guilds/$GID/channels" -H "Authorization: Bearer $ADMIN_TOKEN" \
    -H 'Content-Type: application/json' -d "{\"name\":\"$1\",\"type\":\"VOICE\"}")
  jget "$resp" "d['id']"
}
CH_STAGE=$(make_channel stage-room)
CH_SCREEN=$(make_channel screen-room)
for uid in "$BOT1_ID" "$BOT2_ID"; do
  psql "$DB_URL" -qc "INSERT INTO members (id, guild_id, user_id, nickname, created_at) \
    VALUES (gen_random_uuid(), '$GID', '$uid', '', now())"
done
echo "    bot1=$BOT1_ID bot2=$BOT2_ID guild=$GID stage=$CH_STAGE screen=$CH_SCREEN"

# -----------------------------------------------------------------------------
# 3. 两个 SFU 节点：占位 → enroll → ONLINE → 节点池
# -----------------------------------------------------------------------------
start_sfu() { # start_sfu <序号> <wss端口> <udp端口> <级联端口>
  local idx=$1 wss=$2 udp=$3 cas=$4
  local node node_id enroll_token
  node=$(curl -sf -X POST "$API/admin/sfu/nodes" -H "Authorization: Bearer $ADMIN_TOKEN" \
    -H 'Content-Type: application/json' -d "{\"display_name\":\"stage-node-$idx\"}")
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
  "$WORK/newt-sfu" --config "$WORK/sfu$idx-config.yaml" >"$WORK/sfu$idx.log" 2>&1 &
  eval "SFU${idx}_PID=$!"
  eval "NODE${idx}_ID=$node_id"
  echo "    node$idx=${node_id} (wss:$wss udp:$udp cascade:$cas)"
}

echo "==> 启动两个 SFU 节点"
start_sfu 1 "$SFU1_WSS" "$SFU1_UDP" "$SFU1_CAS"
start_sfu 2 "$SFU2_WSS" "$SFU2_UDP" "$SFU2_CAS"
for i in $(seq 1 60); do
  n=$(curl -sf "$API/admin/sfu/nodes" -H "Authorization: Bearer $ADMIN_TOKEN" \
    | python3 -c "import sys,json; nodes=json.load(sys.stdin); print(sum(1 for n in nodes if n['status']=='ONLINE'))")
  [ "$n" = 2 ] && break
  [ "$i" = 60 ] && { echo "!! 节点未全部上线"; tail -20 "$WORK"/sfu*.log; exit 1; }
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

# =============================================================================
# 场景 A：STAGE 舞台 caps 全链路（docs 11 AD.4；跨节点，caps 经级联同样生效）
# =============================================================================
echo ""
echo "==> [场景 A] 频道 stage-room 设为 STAGE 模式"
curl -sf -X PATCH "$API/channels/$CH_STAGE/voice-stage" -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' -d '{"mode":"STAGE"}' >/dev/null
MODE=$(psql "$DB_URL" -tAc "SELECT mode FROM stage_channel_configs WHERE channel_id='$CH_STAGE'")
[ "$MODE" = "STAGE" ] || { echo "!! 频道模式应为 STAGE，got $MODE"; exit 1; }

BOT_PID=""
start_bot() { # start_bot <bot序号> <频道> <目标节点> <另一节点> <时长> <输出前缀>
  local n=$1 ch=$2 target=$3 other=$4 dur=$5 tag=$6
  "$WORK/loadbot" --server-url "$SERVER_URL" \
    --username "stage-bot$n" --password "password-e2e-2" \
    --guild "$GID" --channel "$ch" \
    --rtt-report "${target}:5,${other}:400" \
    --duration "$dur" --stats-interval 100ms --expect-recv=false \
    >"$WORK/${tag}.out" 2>"$WORK/${tag}.log" &
  BOT_PID=$!
}

echo "==> 两个 AUDIENCE bot 进房（bot1@node1、bot2@node2，均无 publish_audio）"
start_bot 1 "$CH_STAGE" "$NODE1_ID" "$NODE2_ID" 75s a-bot1; BOT1_PID=$BOT_PID
wait_user_on_node "$BOT1_ID" "$NODE1_ID" 20 "bot1 进房"
start_bot 2 "$CH_STAGE" "$NODE2_ID" "$NODE1_ID" 75s a-bot2; BOT2_PID=$BOT_PID
wait_user_on_node "$BOT2_ID" "$NODE2_ID" 20 "bot2 进房"

# join 下发的 caps 必须不含 publish_audio（STAGE AUDIENCE，docs 11 AD.4）。
grep -q '"voice joined"' "$WORK/a-bot1.log" || true
for f in a-bot1 a-bot2; do
  for i in $(seq 1 40); do
    grep -q '"msg":"voice joined"' "$WORK/$f.log" && break; sleep 0.25
  done
done

echo "==> 基线：AUDIENCE 推流 6s，对端必须收不到（SFU 挂起接纳 + 包级门控）"
sleep 6
B1_SENT=$(last_stat "$WORK/a-bot1.out" sent); B1_SENT=${B1_SENT:-0}
B2_RECV=$(last_stat "$WORK/a-bot2.out" recv); B2_RECV=${B2_RECV:-0}
[ "${B1_SENT}" -gt 50 ] || { echo "!! bot1 未在推流（sent=$B1_SENT），无法验证门控"; exit 1; }
[ "${B2_RECV}" -le 5 ] || { echo "!! AUDIENCE 无 publish_audio 但对端已收到 ${B2_RECV} 包"; exit 1; }
echo "    基线 PASS：bot1 sent=${B1_SENT}，bot2 recv=${B2_RECV}（≈0）"

echo "==> bring-up bot1 $(date '+%H:%M:%S')"
UP_MS=$(now_ms)
curl -sf -X POST "$API/channels/$CH_STAGE/stage/bring-up" -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' -d "{\"user_id\":\"$BOT1_ID\"}" >/dev/null
wait_stat_over "$WORK/a-bot2.out" recv $((B2_RECV + 20)) 10 "bot2 开始收到 bot1 音频"
FIRST_MS=$(first_growth_ms "$WORK/a-bot2.out" recv "$B2_RECV")
UP_GAP=$((FIRST_MS - UP_MS))
echo "    bring-up → 对端首批收包 ${UP_GAP}ms（统计采样粒度 100ms）"
[ "$UP_GAP" -ge 0 ] && [ "$UP_GAP" -lt 1000 ] || { echo "!! bring-up 生效 ${UP_GAP}ms ≥ 1000ms"; exit 1; }
SPEAKER=$(psql "$DB_URL" -tAc "SELECT count(*) FROM stage_speakers WHERE channel_id='$CH_STAGE' AND user_id='$BOT1_ID'")
[ "$SPEAKER" = 1 ] || { echo "!! bot1 应为 SPEAKER"; exit 1; }

echo "==> bring-down bot1 $(date '+%H:%M:%S')"
DOWN_MS=$(now_ms)
curl -sf -X POST "$API/channels/$CH_STAGE/stage/bring-down" -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' -d "{\"user_id\":\"$BOT1_ID\"}" >/dev/null
sleep 1.2  # <1s 生效 + 在途包排空缓冲
R_STOP=$(last_stat "$WORK/a-bot2.out" recv); R_STOP=${R_STOP:-0}
sleep 3
R_AFTER=$(last_stat "$WORK/a-bot2.out" recv); R_AFTER=${R_AFTER:-0}
DELTA=$((R_AFTER - R_STOP))
echo "    bring-down 1.2s 后 recv=${R_STOP}，再 3s 后 recv=${R_AFTER}（Δ${DELTA}）"
[ "$DELTA" -le 10 ] || { echo "!! bring-down 后仍在转发（Δ${DELTA} 包/3s）"; exit 1; }

wait "$BOT1_PID" || { echo "!! bot1 退出非零"; tail -5 "$WORK/a-bot1.log"; exit 1; }
wait "$BOT2_PID" || { echo "!! bot2 退出非零"; tail -5 "$WORK/a-bot2.log"; exit 1; }
echo "    [场景 A] PASS（bring-up 生效 ${UP_GAP}ms；bring-down 后 Δ${DELTA} 包/3s）"

# =============================================================================
# 场景 B：屏幕轨结构预留 + 跨节点级联视频冒烟（docs 14 / 15 §7；FREE 频道）
# =============================================================================
echo ""
echo "==> [场景 B] admin@node1 推屏幕轨，bot2@node2 观看（跨级联边）"

# bot2 观看端：/gapi join 落 node2（direct 模式取 token）。
curl -sf -X POST "$GAPI/voice/rtt" -H "Authorization: Bearer $BOT2_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"samples\":[{\"node_id\":\"$NODE2_ID\",\"rtt_ms\":5},{\"node_id\":\"$NODE1_ID\",\"rtt_ms\":400}]}" >/dev/null
J2=$(curl -sf -X POST "$GAPI/voice/join" -H "Authorization: Bearer $BOT2_TOKEN" \
  -H 'Content-Type: application/json' -d "{\"guild_id\":\"$GID\",\"channel_id\":\"$CH_SCREEN\"}")
U2_NODE=$(jget "$J2" "d['node_id']"); U2_WSS=$(jget "$J2" "d['advertise_wss_url']"); T2=$(jget "$J2" "d['token']")
[ "$U2_NODE" = "$NODE2_ID" ] || { echo "!! bot2 应落 node2，got $U2_NODE"; exit 1; }

# admin 发布端：/api join 落 node1。
curl -sf -X POST "$API/voice/rtt" -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"samples\":[{\"node_id\":\"$NODE1_ID\",\"rtt_ms\":5},{\"node_id\":\"$NODE2_ID\",\"rtt_ms\":400}]}" >/dev/null
JA=$(curl -sf -X POST "$API/voice/join" -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' -d "{\"guild_id\":\"$GID\",\"channel_id\":\"$CH_SCREEN\"}")
A_NODE=$(jget "$JA" "d['node_id']"); A_WSS=$(jget "$JA" "d['advertise_wss_url']"); TA1=$(jget "$JA" "d['token']")
[ "$A_NODE" = "$NODE1_ID" ] || { echo "!! admin 应落 node1，got $A_NODE"; exit 1; }
# join token 此刻不应含 publish_screen（未 screen/start 占坑，docs 14 BC.1）。
echo "$JA" | python3 -c "import sys,json; caps=json.load(sys.stdin)['caps']; assert 'publish_screen' not in caps, caps" \
  || { echo "!! 未审批前 caps 不应含 publish_screen"; exit 1; }

echo "==> 观看端 bot2 上线（direct 模式）"
"$WORK/loadbot" --ws-url "$U2_WSS" --token "$T2" --duration 75s --stats-interval 250ms \
  --expect-recv-video \
  >"$WORK/b-bot2.out" 2>"$WORK/b-bot2.log" &
WATCH_PID=$!

echo "==> 阶段 1（负例）：admin 无 publish_screen 推视频轨 10s → 对端必须收不到"
"$WORK/loadbot" --ws-url "$A_WSS" --token "$TA1" --screen --duration 10s \
  --expect-recv=false --stats-interval 250ms \
  >"$WORK/b-adm1.out" 2>"$WORK/b-adm1.log" &
ADM1_PID=$!
wait_stat_over "$WORK/b-bot2.out" recv 20 20 "bot2 收到 admin 音频（互听基线）"
wait "$ADM1_PID" || true
NEG_VIDEO=$(last_stat "$WORK/b-bot2.out" recv_video); NEG_VIDEO=${NEG_VIDEO:-0}
ADM1_SENT_V=$(last_stat "$WORK/b-adm1.out" sent_video); ADM1_SENT_V=${ADM1_SENT_V:-0}
[ "$NEG_VIDEO" -eq 0 ] || { echo "!! 无 publish_screen 时对端收到了 ${NEG_VIDEO} 视频包"; exit 1; }
echo "    负例 PASS：admin sent_video=${ADM1_SENT_V}（轨被 SFU 剥离），bot2 recv_video=0"

echo "==> 阶段 2（正例）：screen/start 占坑 → refresh-token 携带 publish_screen → 重推"
SS=$(curl -sf -X POST "$API/channels/$CH_SCREEN/voice/screen/start" -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' -d '{}')
[ "$(jget "$SS" "d['state']")" = "RESERVED" ] || { echo "!! screen/start 应返回 RESERVED"; exit 1; }
RT=$(curl -sf -X POST "$API/voice/refresh-token" -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' -d "{\"guild_id\":\"$GID\"}")
echo "$RT" | python3 -c "import sys,json; caps=json.load(sys.stdin)['caps']; assert 'publish_screen' in caps, caps" \
  || { echo "!! 占坑后 refresh-token caps 应含 publish_screen"; exit 1; }
TA2=$(jget "$RT" "d['token']")

"$WORK/loadbot" --ws-url "$A_WSS" --token "$TA2" --screen --duration 30s \
  --expect-recv=false --stats-interval 250ms \
  >"$WORK/b-adm2.out" 2>"$WORK/b-adm2.log" &
ADM2_PID=$!

wait_stat_over "$WORK/b-bot2.out" recv_video 100 25 "bot2 跨节点收到屏幕视频包" \
  || { tail -5 "$WORK/b-adm2.log" "$WORK/b-bot2.log"; exit 1; }

# SCREEN_TRACK_ACTIVE 上报链：ScreenSlot RESERVED → ACTIVE（docs 14 BC.1 步骤 5）。
for i in $(seq 1 20); do
  SLOT_STATE=$(psql "$DB_URL" -tAc "SELECT state FROM screen_slots WHERE channel_id='$CH_SCREEN' AND user_id='$ADMIN_ID'")
  [ "$SLOT_STATE" = "ACTIVE" ] && break
  sleep 0.5
done
[ "$SLOT_STATE" = "ACTIVE" ] || { echo "!! ScreenSlot 应转 ACTIVE，got ${SLOT_STATE}"; exit 1; }
echo "    ScreenSlot RESERVED→ACTIVE（SFU SCREEN_TRACK_ACTIVE 上报链 PASS）"

wait "$ADM2_PID" || { echo "!! admin 发布端退出非零"; tail -5 "$WORK/b-adm2.log"; exit 1; }
wait "$WATCH_PID" || { echo "!! bot2 观看端退出非零（未收到视频？）"; tail -5 "$WORK/b-bot2.log"; exit 1; }
FINAL_VIDEO=$(last_stat "$WORK/b-bot2.out" recv_video)
echo "    [场景 B] PASS（bot2 跨节点 recv_video=${FINAL_VIDEO}）"

echo ""
echo "=========================================="
echo "M5 舞台/屏幕 E2E 全部通过："
echo "  场景 A STAGE caps: AUDIENCE 静默（recv≈${B2_RECV}）→ bring-up 生效 ${UP_GAP}ms（<1s）"
echo "                     → bring-down 后 Δ${DELTA} 包/3s（<1s 停止）"
echo "  场景 B 屏幕轨:     无 cap 剥离（recv_video=0）→ 占坑+refresh 后跨节点转发"
echo "                     recv_video=${FINAL_VIDEO}，ScreenSlot ACTIVE"
echo "数据库: ${DB_NAME}  日志目录: ${WORK}"
echo "=========================================="
