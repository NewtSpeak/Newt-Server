#!/usr/bin/env bash
# =============================================================================
# M4 热迁移端到端联调脚本（叶死 / Drain / 根死切根，docs 09 + 08 §7.1）
#
# 链路：Newt-Server（迁移状态机 + 调度 + 级联编排 + Gateway）
#       ←mTLS gRPC→ Newt-SFU ×2（MigrateParticipants MARK/EXECUTE + 双会话共存）
#       ← Gateway WS / WebRTC → cmd/loadbot ×2（--server-url 模式：
#          登录 → voice/join → Gateway 收 VOICE_MIGRATING/VOICE_SERVER_UPDATE
#          → 双 PC 热切 → ack → 输出 mute_gap_ms）
#
# 验收点（docs 15 BM M4：叶死/根死静音窗口达标；任务卡三场景）：
#   1. 叶死：kill -9 非 anchor 节点 → BI.3 提前判死（级联邻居 EdgeDown 指控 +
#      客户端 ice-failed 上报 + ≥1 次心跳丢失，docs 15 §5）→ 该节点用户自动迁到
#      存活节点、音频自动恢复（loadbot 不重启、无人工操作），mute_gap < 10s
#   2. Drain：admin drain → 用户秒级主动迁走，mute_gap < 5s
#   3. 根死：kill -9 anchor → 新 anchor 选举 + epoch+1 + 原根用户迁移 →
#      双方恢复互听；断言 DB lease 换根且 epoch+1
#   4. 迁移后 caps 重放：迁移后仍能 publish/subscribe（双方 recv 恢复增长）
#
# 前置：本机 PostgreSQL（docker owl-server-postgres-1）、Go 工具链、python3、psql。
# 每次运行新建独立数据库 owl_e2e_mig_<时间戳>，不触碰既有库。
# =============================================================================
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SFU_DIR="${SFU_DIR:-$(cd "$ROOT/.." && pwd)/Newt-SFU}"

PG_ADMIN_URL="${PG_ADMIN_URL:-postgres://owl:owl_dev_password@127.0.0.1:5432/owl?sslmode=disable}"
DB_NAME="owl_e2e_mig_$(date +%s)"
DB_URL="postgres://owl:owl_dev_password@127.0.0.1:5432/${DB_NAME}?sslmode=disable"

APP_PORT=18082
GRPC_PORT=19446
SFU1_WSS=18447; SFU1_UDP=13480; SFU1_CAS=18845
SFU2_WSS=18448; SFU2_UDP=13481; SFU2_CAS=18846
API="http://127.0.0.1:${APP_PORT}/api/v1"
GAPI="http://127.0.0.1:${APP_PORT}/gapi/v1"
SERVER_URL="http://127.0.0.1:${APP_PORT}"

WORK="$(mktemp -d /tmp/owl-e2e-migration.XXXXXX)"
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

# 读取某 bot stdout 中最近一条统计行的 recv 计数（键按字母序序列化）。
# 注意 set -o pipefail：无匹配时 grep 退出码非零，必须吞掉，否则命令替换会
# 触发 set -e 静默退出。
last_recv() { { grep -o '"recv":[0-9]*' "$1" 2>/dev/null || true; } | tail -1 | cut -d: -f2; }

# 等待某 bot 的 recv 超过阈值（互听 / 恢复判据）
wait_recv() { # wait_recv <out文件> <阈值> <超时秒> <描述>
  local f=$1 min=$2 timeout=$3 desc=$4 i r
  for i in $(seq 1 $((timeout * 2))); do
    r=$(last_recv "$f"); r=${r:-0}
    [ "$r" -gt "$min" ] && { echo "    ${desc}: recv=${r}"; return 0; }
    sleep 0.5
  done
  echo "!! ${desc} 超时（recv=$(last_recv "$f")，期望 > ${min}）"
  return 1
}

# 等待某 bot 输出 migrated 事件并回显 mute_gap_ms
wait_migrated() { # wait_migrated <out文件> <超时秒>
  local f=$1 timeout=$2 i line
  for i in $(seq 1 $((timeout * 2))); do
    line=$({ grep '"event":"migrated"' "$f" 2>/dev/null || true; } | tail -1)
    if [ -n "$line" ]; then
      jget "$line" "d['mute_gap_ms']"
      return 0
    fi
    sleep 0.5
  done
  echo "-1"
  return 1
}

# 断言迁移后音频恢复增长（caps 重放：仍能 publish/subscribe）
assert_recovery() { # assert_recovery <out文件> <描述>
  local f=$1 desc=$2 r1 r2
  r1=$(last_recv "$f"); r1=${r1:-0}
  sleep 5
  r2=$(last_recv "$f"); r2=${r2:-0}
  if [ $((r2 - r1)) -le 50 ]; then
    echo "!! ${desc} 迁移后音频未恢复（recv ${r1} → ${r2}）"
    return 1
  fi
  echo "    ${desc} 音频恢复增长: recv ${r1} → ${r2}（Δ$((r2 - r1))/5s）"
}

user_node() { # user_node <user_id> → 当前 VoiceState.node_id
  psql "$DB_URL" -tAc "SELECT node_id FROM voice_states WHERE user_id = '$1' AND channel_id IS NOT NULL"
}

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
# 2. 账号（admin + 两个 bot 用户）+ Guild + 三个语音频道（每场景一间房）
# -----------------------------------------------------------------------------
echo "==> 注册账号与频道"
ADMIN_RESP=$(curl -sf -X POST "$API/auth/register" -H 'Content-Type: application/json' \
  -d '{"username":"mig-admin","email":"mig-admin@test.dev","password":"password-e2e-1"}')
ADMIN_TOKEN=$(jget "$ADMIN_RESP" "d['access_token']")

BOT1_RESP=$(curl -sf -X POST "$GAPI/auth/signup" -H 'Content-Type: application/json' \
  -d '{"username":"mig-bot1","email":"mig-bot1@test.dev","password":"password-e2e-2"}')
BOT1_ID=$(jget "$BOT1_RESP" "d['user']['id']")
BOT2_RESP=$(curl -sf -X POST "$GAPI/auth/signup" -H 'Content-Type: application/json' \
  -d '{"username":"mig-bot2","email":"mig-bot2@test.dev","password":"password-e2e-2"}')
BOT2_ID=$(jget "$BOT2_RESP" "d['user']['id']")

GUILD=$(curl -sf -X POST "$API/guilds" -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"e2e-migration"}')
GID=$(jget "$GUILD" "d['id']")
make_channel() {
  local resp
  resp=$(curl -sf -X POST "$API/guilds/$GID/channels" -H "Authorization: Bearer $ADMIN_TOKEN" \
    -H 'Content-Type: application/json' -d "{\"name\":\"$1\",\"type\":\"VOICE\"}")
  jget "$resp" "d['id']"
}
CH1=$(make_channel mig-leaf-death)
CH2=$(make_channel mig-drain)
CH3=$(make_channel mig-root-death)
for uid in "$BOT1_ID" "$BOT2_ID"; do
  psql "$DB_URL" -qc "INSERT INTO members (id, guild_id, user_id, nickname, created_at) \
    VALUES (gen_random_uuid(), '$GID', '$uid', '', now())"
done
echo "    bot1=$BOT1_ID bot2=$BOT2_ID guild=$GID ch1=$CH1 ch2=$CH2 ch3=$CH3"

# -----------------------------------------------------------------------------
# 3. 两个 SFU 节点：占位 → enroll → ONLINE → 节点池
# -----------------------------------------------------------------------------
start_sfu() { # start_sfu <序号> <wss端口> <udp端口> <级联端口>（首次：建节点 + 写配置）
  local idx=$1 wss=$2 udp=$3 cas=$4
  local node node_id enroll_token
  node=$(curl -sf -X POST "$API/admin/sfu/nodes" -H "Authorization: Bearer $ADMIN_TOKEN" \
    -H 'Content-Type: application/json' -d "{\"display_name\":\"mig-node-$idx\"}")
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
  launch_sfu "$idx"
  eval "NODE${idx}_ID=$node_id"
  echo "    node$idx=${node_id} (wss:$wss udp:$udp cascade:$cas)"
}

launch_sfu() { # launch_sfu <序号>：按既有配置（含已落盘证书）拉起进程
  local idx=$1
  "$WORK/owl-sfu" --config "$WORK/sfu$idx-config.yaml" >>"$WORK/sfu$idx.log" 2>&1 &
  eval "SFU${idx}_PID=$!"
}

wait_online() { # wait_online <期望 ONLINE 数>
  local want=$1 i n
  for i in $(seq 1 60); do
    n=$(curl -sf "$API/admin/sfu/nodes" -H "Authorization: Bearer $ADMIN_TOKEN" \
      | python3 -c "import sys,json; nodes=json.load(sys.stdin); print(sum(1 for n in nodes if n['status']=='ONLINE'))")
    [ "$n" = "$want" ] && return 0
    sleep 0.5
  done
  echo "!! 节点未全部上线（期望 $want）"; tail -20 "$WORK"/sfu*.log
  return 1
}

echo "==> 启动两个 SFU 节点"
start_sfu 1 "$SFU1_WSS" "$SFU1_UDP" "$SFU1_CAS"
start_sfu 2 "$SFU2_WSS" "$SFU2_UDP" "$SFU2_CAS"
wait_online 2
echo "    两节点 ONLINE"

for nid in "$NODE1_ID" "$NODE2_ID"; do
  curl -sf -X PATCH "$API/admin/sfu/nodes/$nid" -H "Authorization: Bearer $ADMIN_TOKEN" \
    -H 'Content-Type: application/json' -d '{"enabled_for_scheduling":true}' >/dev/null
done
curl -sf -X PUT "$API/admin/guilds/$GID/node-pool" -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"candidate_node_ids\":[\"$NODE1_ID\",\"$NODE2_ID\"],\"selected_node_ids\":[\"$NODE1_ID\",\"$NODE2_ID\"]}" >/dev/null
echo "    调度开启 + 节点池 = {node1, node2}"

# 启动 bot（--rtt-report 引导调度落点：目标节点 5ms / 另一节点 400ms）。
# 注意：必须在当前 shell 内后台启动（不能用命令替换的子 shell），
# 否则 bot 不是本 shell 的子进程，后面 wait 不到。PID 经 BOT_PID 全局变量带出。
BOT_PID=""
start_bot() { # start_bot <bot序号> <频道> <目标节点> <另一节点> <时长> <输出前缀>
  local n=$1 ch=$2 target=$3 other=$4 dur=$5 tag=$6
  "$WORK/loadbot" --server-url "$SERVER_URL" \
    --username "mig-bot$n" --password "password-e2e-2" \
    --guild "$GID" --channel "$ch" \
    --rtt-report "${target}:5,${other}:400" \
    --duration "$dur" \
    >"$WORK/${tag}.out" 2>"$WORK/${tag}.log" &
  BOT_PID=$!
}

# =============================================================================
# 场景 1：叶死迁移（docs 09 I.1 / 08 §7.2 / 15 §5 BI.3 提前判死）：
# kill 后级联邻居 EdgeDown（~1s）+ bot 侧 ice-failed 上报（~即时）+ ≥1 次心跳
# 丢失（≤5s）→ 提前判死（无需等满 15s）→ 迁移 1–2s，mute_gap 目标 < 10s。
# =============================================================================
echo ""
echo "==> [场景 1] 叶死迁移：bot1@node1(anchor)、bot2@node2(叶)，kill -9 node2"
start_bot 1 "$CH1" "$NODE1_ID" "$NODE2_ID" 90s s1-bot1; BOT1_PID=$BOT_PID
wait_user_on_node "$BOT1_ID" "$NODE1_ID" 20 "bot1 进房（首人 = anchor）"
start_bot 2 "$CH1" "$NODE2_ID" "$NODE1_ID" 90s s1-bot2; BOT2_PID=$BOT_PID
wait_user_on_node "$BOT2_ID" "$NODE2_ID" 20 "bot2 进房（叶节点）"
ANCHOR=$(psql "$DB_URL" -tAc "SELECT anchor_node_id FROM voice_anchor_leases WHERE room_id='$CH1'")
[ "$ANCHOR" = "$NODE1_ID" ] || { echo "!! anchor 应为 node1，got $ANCHOR"; exit 1; }

wait_recv "$WORK/s1-bot1.out" 100 30 "bot1 互听基线"
wait_recv "$WORK/s1-bot2.out" 100 30 "bot2 互听基线"

echo "    kill -9 node2（叶）$(date '+%H:%M:%S')"
KILL_MS=$(now_ms)
kill -9 "$SFU2_PID"

GAP1=$(wait_migrated "$WORK/s1-bot2.out" 40) || { echo "!! bot2 未完成迁移"; tail -5 "$WORK/s1-bot2.log"; exit 1; }
echo "    bot2 迁移完成，mute_gap_ms=${GAP1}（kill 后 $((($(now_ms) - KILL_MS)))ms）"
# BI.3 提前判死生效断言：静音窗口显著低于硬判死路径（15s 判死 + 迁移）。
[ "$GAP1" -ge 0 ] && [ "$GAP1" -lt 10000 ] || { echo "!! 叶死静音窗口 ${GAP1}ms ≥ 10000ms（提前判死未生效？）"; exit 1; }
grep -q '"event":"ice_failed_report"' "$WORK/s1-bot2.out" \
  && echo "    bot2 已上报 ice-failed（BI.3 客户端侧信号）" \
  || echo "    （注：bot2 未见 ice-failed 上报行，提前判死依赖 EdgeDown+self_ice 信号）"
wait_user_on_node "$BOT2_ID" "$NODE1_ID" 10 "bot2 迁移落点"
assert_recovery "$WORK/s1-bot2.out" "bot2（迁移方）"
assert_recovery "$WORK/s1-bot1.out" "bot1（对端）"

wait "$BOT1_PID" || { echo "!! bot1 退出非零"; tail -5 "$WORK/s1-bot1.log"; exit 1; }
wait "$BOT2_PID" || { echo "!! bot2 退出非零"; tail -5 "$WORK/s1-bot2.log"; exit 1; }
echo "    [场景 1] PASS（mute_gap_ms=${GAP1}）"

echo "==> 重启 node2 并等待 ONLINE"
launch_sfu 2
wait_online 2

# =============================================================================
# 场景 2：Drain 迁移（docs 09 I.6；无需判死，秒级，mute_gap < 5s）
# =============================================================================
echo ""
echo "==> [场景 2] Drain 迁移：bot1@node1、bot2@node2，admin drain node2"
start_bot 1 "$CH2" "$NODE1_ID" "$NODE2_ID" 60s s2-bot1; BOT1_PID=$BOT_PID
wait_user_on_node "$BOT1_ID" "$NODE1_ID" 20 "bot1 进房"
start_bot 2 "$CH2" "$NODE2_ID" "$NODE1_ID" 60s s2-bot2; BOT2_PID=$BOT_PID
wait_user_on_node "$BOT2_ID" "$NODE2_ID" 20 "bot2 进房"
wait_recv "$WORK/s2-bot1.out" 100 30 "bot1 互听基线"
wait_recv "$WORK/s2-bot2.out" 100 30 "bot2 互听基线"

echo "    admin drain node2 $(date '+%H:%M:%S')"
DRAIN_MS=$(now_ms)
curl -sf -X POST "$API/admin/sfu/nodes/$NODE2_ID/drain" -H "Authorization: Bearer $ADMIN_TOKEN" >/dev/null

GAP2=$(wait_migrated "$WORK/s2-bot2.out" 20) || { echo "!! bot2 未完成 Drain 迁移"; tail -5 "$WORK/s2-bot2.log"; exit 1; }
echo "    bot2 迁移完成，mute_gap_ms=${GAP2}（drain 后 $((($(now_ms) - DRAIN_MS)))ms）"
[ "$GAP2" -ge 0 ] && [ "$GAP2" -lt 5000 ] || { echo "!! Drain 静音窗口 ${GAP2}ms ≥ 5000ms"; exit 1; }
wait_user_on_node "$BOT2_ID" "$NODE1_ID" 10 "bot2 迁移落点"
assert_recovery "$WORK/s2-bot2.out" "bot2（迁移方）"
assert_recovery "$WORK/s2-bot1.out" "bot1（对端）"

wait "$BOT1_PID" || { echo "!! bot1 退出非零"; tail -5 "$WORK/s2-bot1.log"; exit 1; }
wait "$BOT2_PID" || { echo "!! bot2 退出非零"; tail -5 "$WORK/s2-bot2.log"; exit 1; }
echo "    [场景 2] PASS（mute_gap_ms=${GAP2}）"

echo "==> undrain node2 并等待 ONLINE"
curl -sf -X POST "$API/admin/sfu/nodes/$NODE2_ID/undrain" -H "Authorization: Bearer $ADMIN_TOKEN" >/dev/null
wait_online 2

# =============================================================================
# 场景 3：根死切根（docs 08 §7.1 + 09 L.2；新 anchor + epoch+1 + 原根用户迁移）
# =============================================================================
echo ""
echo "==> [场景 3] 根死切根：bot1@node1(anchor)、bot2@node2，kill -9 node1"
start_bot 1 "$CH3" "$NODE1_ID" "$NODE2_ID" 90s s3-bot1; BOT1_PID=$BOT_PID
wait_user_on_node "$BOT1_ID" "$NODE1_ID" 20 "bot1 进房（首人 = anchor）"
start_bot 2 "$CH3" "$NODE2_ID" "$NODE1_ID" 90s s3-bot2; BOT2_PID=$BOT_PID
wait_user_on_node "$BOT2_ID" "$NODE2_ID" 20 "bot2 进房"
EPOCH_BEFORE=$(psql "$DB_URL" -tAc "SELECT epoch FROM voice_anchor_leases WHERE room_id='$CH3'")
ANCHOR=$(psql "$DB_URL" -tAc "SELECT anchor_node_id FROM voice_anchor_leases WHERE room_id='$CH3'")
[ "$ANCHOR" = "$NODE1_ID" ] || { echo "!! anchor 应为 node1，got $ANCHOR"; exit 1; }
echo "    切根前：anchor=node1 epoch=${EPOCH_BEFORE}"

wait_recv "$WORK/s3-bot1.out" 100 30 "bot1 互听基线"
wait_recv "$WORK/s3-bot2.out" 100 30 "bot2 互听基线"

echo "    kill -9 node1（anchor）$(date '+%H:%M:%S')"
KILL_MS=$(now_ms)
kill -9 "$SFU1_PID"

GAP3=$(wait_migrated "$WORK/s3-bot1.out" 40) || { echo "!! bot1 未完成根死迁移"; tail -5 "$WORK/s3-bot1.log"; exit 1; }
echo "    bot1 迁移完成，mute_gap_ms=${GAP3}（kill 后 $((($(now_ms) - KILL_MS)))ms）"
[ "$GAP3" -ge 0 ] && [ "$GAP3" -lt 25000 ] || { echo "!! 根死静音窗口 ${GAP3}ms ≥ 25000ms"; exit 1; }

# 切根断言：lease 换根到 node2 且 epoch 严格 +1（docs 08 §3.1 / §7.1）
LEASE=$(psql "$DB_URL" -tAc "SELECT anchor_node_id||' '||epoch FROM voice_anchor_leases WHERE room_id='$CH3'")
NEW_ANCHOR=$(echo "$LEASE" | awk '{print $1}')
EPOCH_AFTER=$(echo "$LEASE" | awk '{print $2}')
[ "$NEW_ANCHOR" = "$NODE2_ID" ] || { echo "!! 新 anchor 应为 node2，got $NEW_ANCHOR"; exit 1; }
[ "$EPOCH_AFTER" = "$((EPOCH_BEFORE + 1))" ] || { echo "!! epoch 应为 $((EPOCH_BEFORE + 1))，got $EPOCH_AFTER"; exit 1; }
echo "    切根断言 PASS：anchor node1 → node2，epoch ${EPOCH_BEFORE} → ${EPOCH_AFTER}"
wait_user_on_node "$BOT1_ID" "$NODE2_ID" 10 "bot1（原根用户）迁移落点"

# 双方恢复互听（迁移后 caps 重放：仍能 publish/subscribe）
assert_recovery "$WORK/s3-bot1.out" "bot1（迁移方）"
assert_recovery "$WORK/s3-bot2.out" "bot2（对端）"

wait "$BOT1_PID" || { echo "!! bot1 退出非零"; tail -5 "$WORK/s3-bot1.log"; exit 1; }
wait "$BOT2_PID" || { echo "!! bot2 退出非零"; tail -5 "$WORK/s3-bot2.log"; exit 1; }
echo "    [场景 3] PASS（mute_gap_ms=${GAP3}）"

echo ""
echo "=========================================="
echo "M4 热迁移 E2E 全部通过："
echo "  场景 1 叶死:   mute_gap_ms=${GAP1}（断言 < 10000，BI.3 提前判死）"
echo "  场景 2 Drain:  mute_gap_ms=${GAP2}（断言 < 5000）"
echo "  场景 3 根死:   mute_gap_ms=${GAP3}（断言 < 25000）+ 切根 epoch ${EPOCH_BEFORE}→${EPOCH_AFTER}"
echo "  迁移后 caps 重放（publish/subscribe 恢复）三场景均验证"
echo "数据库: ${DB_NAME}  日志目录: ${WORK}"
echo "=========================================="
