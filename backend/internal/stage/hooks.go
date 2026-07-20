package stage

import (
	"log"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/sfuctl"
	"gorm.io/gorm"
)

// 本文件是舞台模块对外暴露的进出房/媒体钩子。语音编排模块在进房/离房路径直接调用
// OnVoiceJoin / OnVoiceLeave 可获得同步的容量禁说与强制 STAGE 处理；
// 即便不接，本包也订阅 Bus 的 VOICE_STATE_UPDATE 事件兜底（见 register.go）。

// OnVoiceJoin 用户进入语音频道后调用：>50 强制 STAGE、第 51+ 人容量禁说 + 自动入队（docs 11 Z）。
// db/bus 参数保留以兼容调用方签名要求；Register 前调用为 no-op。
var OnVoiceJoin = func(db *gorm.DB, bus *eventbus.Bus, guildID, channelID, userID uuid.UUID) {
	log.Printf("stage: OnVoiceJoin 尚未装配（channel=%s user=%s）", channelID, userID)
}

// OnVoiceLeave 用户离开语音频道后调用：FIFO 解除最早容量禁说者、释放席位/队位/屏幕坑。
var OnVoiceLeave = func(db *gorm.DB, bus *eventbus.Bus, guildID, channelID, userID uuid.UUID) {
	log.Printf("stage: OnVoiceLeave 尚未装配（channel=%s user=%s）", channelID, userID)
}

// OnScreenTrackActive SFU 上报屏幕轨发布成功后调用：RESERVED → ACTIVE 并广播
// SCREEN_SHARE_START（docs 14 BC.1 步骤 5–6）。由 sfunode/语音编排模块在收到上报时接入。
var OnScreenTrackActive = func(db *gorm.DB, bus *eventbus.Bus, channelID, userID uuid.UUID) {
	log.Printf("stage: OnScreenTrackActive 尚未装配（channel=%s user=%s）", channelID, userID)
}

// sfuDirPoolNodes 节点池负载查询的可替换入口（单测中替换为假数据）。
var sfuDirPoolNodes = func(guildID uuid.UUID) ([]sfuctl.NodeInfo, error) {
	return sfuctl.Dir().PoolNodes(guildID)
}
