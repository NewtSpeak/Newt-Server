package gateway

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/owlspeak/owl-server/backend/internal/presence"
	"github.com/owlspeak/owl-server/backend/internal/snapshot"
)

// options 协议时序与回放参数（单测缩短超时用；生产默认见 defaultOptions）。
type options struct {
	HeartbeatInterval time.Duration // HELLO 下发的心跳周期；两个周期无心跳判死
	IdentifyTimeout   time.Duration // 连接建立后等待 IDENTIFY/RESUME 的时限
	WriteTimeout      time.Duration // 单帧写超时
	SendBuffer        int           // 每连接发送队列容量，积压满断开
	ReplayBufferSize  int           // 每会话事件回放缓冲条数上限（docs 14 §7-4）
	ReplayTTL         time.Duration // 回放缓冲条目最长保留时间
	ResumeWindow      time.Duration // 连接断开后会话保留时长（等待 RESUME）
	SweepInterval     time.Duration // 过期会话清扫周期
}

func defaultOptions() options {
	return options{
		HeartbeatInterval: 30 * time.Second,
		IdentifyTimeout:   10 * time.Second,
		WriteTimeout:      10 * time.Second,
		SendBuffer:        256,
		ReplayBufferSize:  512,
		ReplayTTL:         5 * time.Minute,
		ResumeWindow:      3 * time.Minute,
		SweepInterval:     30 * time.Second,
	}
}

// handler Gateway WebSocket 端点：握手状态机 + 会话注册。
type handler struct {
	auth     authenticator
	hub      *hub
	opts     options
	upgrader websocket.Upgrader
	// presence 在线状态注册表（可为 nil=不启用）：IDENTIFY/RESUME 登记连接、
	// 上行 PRESENCE_UPDATE 设置状态、READY 快照附带各 guild 在线成员状态。
	presence *presence.Manager
}

func newHandler(auth authenticator, dir directory, opts options) *handler {
	h := &handler{
		auth: auth,
		hub:  newHub(dir, opts),
		opts: opts,
		// 鉴权靠 IDENTIFY 携带 access token 而非 Cookie，不存在跨站会话劫持面，
		// 因此放开 Origin 校验（浏览器 WS 无法自定义 header，见 docs 05 §1 通道分工）。
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
	}
	go h.hub.sweepLoop(opts.SweepInterval)
	return h
}

// attachPresence 注入 presence 注册表（须在 serve 接客前调用）。
func (h *handler) attachPresence(p *presence.Manager) {
	h.presence = p
	h.hub.presence = p
}

// serve GET /gateway：升级为 WebSocket 后按协议推进握手与心跳循环。
// 握手阶段（HELLO → IDENTIFY/RESUME → READY/补发+RESUMED）由本 goroutine 直接写帧，
// 保证快照/补发先于并发 dispatch 送达；握手完成后再启动 writePump 消费发送队列。
func (h *handler) serve(c *gin.Context) {
	ws, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return // Upgrade 失败时 gorilla 已写回 HTTP 错误
	}
	conn := newConn(ws, h.opts.SendBuffer)
	if !h.writeDirect(conn, outFrame{Op: opHello, D: helloData{HeartbeatIntervalMS: h.opts.HeartbeatInterval.Milliseconds()}}) {
		return
	}
	sess, ok := h.handshake(conn)
	if !ok {
		return
	}
	go conn.writePump(h.opts.WriteTimeout)
	defer sess.detach(conn)
	h.readLoop(conn, sess)
}

// writeDirect 握手阶段的同步写（writePump 尚未启动，本 goroutine 为唯一写者）。
func (h *handler) writeDirect(c *conn, frame any) bool {
	raw, err := json.Marshal(frame)
	if err != nil {
		c.shutdown(websocket.CloseInternalServerErr, "序列化失败", h.opts.WriteTimeout)
		return false
	}
	return h.writeDirectRaw(c, raw)
}

func (h *handler) writeDirectRaw(c *conn, raw []byte) bool {
	_ = c.ws.SetWriteDeadline(time.Now().Add(h.opts.WriteTimeout))
	if err := c.ws.WriteMessage(websocket.TextMessage, raw); err != nil {
		c.shutdown(websocket.CloseInternalServerErr, "写入失败", h.opts.WriteTimeout)
		return false
	}
	return true
}

// handshake 等待并处理首条 IDENTIFY（全量同步）或 RESUME（断线续传）。
func (h *handler) handshake(c *conn) (*session, bool) {
	_ = c.ws.SetReadDeadline(time.Now().Add(h.opts.IdentifyTimeout))
	_, raw, err := c.ws.ReadMessage()
	if err != nil {
		c.shutdown(closeIdentifyTimeout, "超时未 IDENTIFY", h.opts.WriteTimeout)
		return nil, false
	}
	var f inFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		c.shutdown(closeIdentifyTimeout, "首帧必须为 IDENTIFY 或 RESUME", h.opts.WriteTimeout)
		return nil, false
	}
	switch f.Op {
	case opIdentify:
		return h.identify(c, f.D)
	case opResume:
		return h.resume(c, f.D)
	default:
		c.shutdown(closeIdentifyTimeout, "首帧必须为 IDENTIFY 或 RESUME", h.opts.WriteTimeout)
		return nil, false
	}
}

// identify 校验 IDENTIFY：认证成功则创建新会话并下发 READY 全量快照。
func (h *handler) identify(c *conn, data json.RawMessage) (*session, bool) {
	var input identifyData
	if err := json.Unmarshal(data, &input); err != nil || input.Token == "" {
		c.shutdown(closeAuthFailed, "缺少访问令牌", h.opts.WriteTimeout)
		return nil, false
	}
	user, guildIDs, err := h.auth.Authenticate(input.Token)
	if err != nil {
		c.shutdown(closeAuthFailed, "无效的访问令牌", h.opts.WriteTimeout)
		return nil, false
	}
	guilds, err := h.hub.dir.GuildSnapshots(user, guildIDs)
	if err != nil {
		c.shutdown(websocket.CloseInternalServerErr, "组装 READY 快照失败", h.opts.WriteTimeout)
		return nil, false
	}
	sess := &session{id: uuid.NewString(), user: user, conn: c}
	h.hub.register(sess)
	if h.presence != nil {
		// 先登记 presence（默认 online，会触发本人/他人 PRESENCE_UPDATE 广播），
		// 再组装 READY presences，保证快照里能看到自己。事件帧只会进入发送队列
		//（writePump 在握手完成后才启动），不会先于 READY 送达。
		h.presence.Connect(user.ID, sess.id)
		for i := range guilds {
			memberIDs, err := h.hub.dir.GuildMemberIDs(guilds[i].Guild.ID)
			if err != nil {
				continue // 成员查询失败只影响该 guild 的 presences，快照主体照常下发
			}
			infos := h.presence.Displayed(user.ID, memberIDs)
			presences := make([]snapshot.Presence, 0, len(infos))
			for _, memberID := range memberIDs {
				if info, ok := infos[memberID]; ok {
					presences = append(presences, snapshot.Presence{UserID: memberID, Status: info.Status, CustomText: info.CustomText})
				}
			}
			guilds[i].Presences = presences
		}
	}
	// read_states 按快照内的可见频道集合过滤（不可见/禁看频道的存量记录不下发）。
	visibleChannelIDs := make([]uuid.UUID, 0, 32)
	for i := range guilds {
		for _, channel := range guilds[i].Channels {
			visibleChannelIDs = append(visibleChannelIDs, channel.ID)
		}
	}
	readStates, err := h.hub.dir.ReadStates(user.ID, visibleChannelIDs)
	if err != nil {
		// 已读状态查询失败不阻断连接：客户端可经 GET /users/@me/read-states 兜底校正。
		readStates = []snapshot.ReadState{}
	}
	ready := readyData{
		SessionID:  sess.id,
		User:       user,
		GuildIDs:   guildIDs,
		Guilds:     guilds,
		ReadStates: readStates,
	}
	if !h.writeDirect(c, outFrame{Op: opReady, D: ready}) {
		sess.detach(c)
		return nil, false
	}
	return sess, true
}

// resume 校验 RESUME：token 认证 + 会话归属校验 + 回放窗口校验；
// 可补发则按序补发缺口事件后发 RESUMED，否则回 INVALID_SESSION 并以 4009 关闭
//（客户端应重新 IDENTIFY 做全量同步，docs 14 FR-04）。
func (h *handler) resume(c *conn, data json.RawMessage) (*session, bool) {
	var input resumeData
	if err := json.Unmarshal(data, &input); err != nil || input.Token == "" || input.SessionID == "" {
		c.shutdown(closeAuthFailed, "缺少访问令牌或会话 ID", h.opts.WriteTimeout)
		return nil, false
	}
	user, _, err := h.auth.Authenticate(input.Token)
	if err != nil {
		c.shutdown(closeAuthFailed, "无效的访问令牌", h.opts.WriteTimeout)
		return nil, false
	}
	sess, found := h.hub.findSession(input.SessionID)
	if !found || sess.user.ID != user.ID {
		h.invalidSession(c, "会话不存在或已过期")
		return nil, false
	}
	frames, replaced, ok := sess.resumeAttach(input.LastSeq, c)
	if !ok {
		h.invalidSession(c, "超出回放窗口，无法补发")
		return nil, false
	}
	// 对冲清扫竞态：attach 成功后重新登记（register 幂等）。
	h.hub.register(sess)
	if h.presence != nil {
		// 幂等补登记：正常 resume 时 presence 仍持有该会话（保留原状态不被重置）；
		// 若恰被清扫过，则按新连接重新上线。
		h.presence.Connect(sess.user.ID, sess.id)
	}
	if replaced != nil && replaced != c {
		replaced.shutdown(closeSessionReplaced, "会话被新连接接管", h.opts.WriteTimeout)
	}
	for _, frame := range frames {
		if !h.writeDirectRaw(c, frame) {
			sess.detach(c)
			return nil, false
		}
	}
	if !h.writeDirect(c, outFrame{Op: opResumed}) {
		sess.detach(c)
		return nil, false
	}
	return sess, true
}

// invalidSession 下发 INVALID_SESSION 帧后以 4009 关闭。
func (h *handler) invalidSession(c *conn, reason string) {
	_ = h.writeDirect(c, outFrame{Op: opInvalidSession})
	c.shutdown(closeInvalidSession, reason, h.opts.WriteTimeout)
}

// readLoop 认证后的读循环：处理心跳与 PRESENCE_UPDATE 上行，连续两个周期无消息判死断开。
func (h *handler) readLoop(c *conn, sess *session) {
	ack, _ := json.Marshal(outFrame{Op: opHeartbeatACK})
	for {
		_ = c.ws.SetReadDeadline(time.Now().Add(2 * h.opts.HeartbeatInterval))
		_, raw, err := c.ws.ReadMessage()
		if err != nil {
			c.shutdown(closeHeartbeatDead, "心跳超时或连接断开", h.opts.WriteTimeout)
			return
		}
		var f inFrame
		if err := json.Unmarshal(raw, &f); err != nil {
			continue // 容忍无法解析的上行帧，仅依赖读超时判死
		}
		switch f.Op {
		case opHeartbeat:
			if !c.enqueue(ack) {
				c.shutdown(closeSlowConsumer, "消息积压", h.opts.WriteTimeout)
				return
			}
		case opPresenceUpdate:
			if h.presence == nil {
				continue
			}
			var input presenceUpdateData
			if err := json.Unmarshal(f.D, &input); err != nil {
				continue
			}
			// 非法 status 由 SetStatus 拒绝（返回 false），与无法解析的帧同样静默忽略。
			h.presence.SetStatus(sess.user.ID, sess.id, input.Status, input.CustomText)
		}
	}
}
