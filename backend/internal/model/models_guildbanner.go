package model

import (
	"time"

	"github.com/google/uuid"
)

// GuildBanner 服务器 banner（每服多张、position 有序，guildapi 服务器外观专项）。
//
// 取舍说明：不引用 message.Attachment——附件基建的 GC 会清理「未绑定消息」的
// 附件记录（24h），且消息保留策略/软删清理都会连带删附件，banner 生命周期与
// 消息完全不同（随服存续、由服管显式增删），混用会被消息清理误删。
// 二进制文件复用服务器图标/横幅既有的公开资产存储约定：
// DataDir/profile 目录 + /public-assets/profile/{name} 公开访问
//（文件名带 banner ID 与纳秒版本号，不可枚举且不可变，允许长缓存）。
type GuildBanner struct {
	ID      uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	GuildID uuid.UUID `gorm:"type:uuid;not null;index:idx_guild_banner_guild" json:"guild_id"`
	// URL 公开访问路径（/public-assets/profile/...），生成后不可变。
	URL string `gorm:"size:512;not null" json:"url"`
	// Position 展示顺序（0 起升序）；重排序时整表重新编号，无空洞。
	Position  int       `gorm:"not null;default:0" json:"position"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func init() {
	Register(&GuildBanner{})
}
