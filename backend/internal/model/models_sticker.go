package model

import (
	"time"

	"github.com/google/uuid"
)

// ================= 贴图与表情包（docs 17）=================
// 包跟账号；可选服独属（scope=guild）；kind=emote|sticker 创建后锁定。
// ID 均为雪花 int64（与消息 ID 同族），JSON 以字符串输出。

// StickerPackScope 包作用域。
type StickerPackScope string

const (
	StickerScopeAccount StickerPackScope = "account"
	StickerScopeGuild   StickerPackScope = "guild"
)

// StickerKind 包/条目类型：小表情可图文混排；贴图独立消息、禁止混排。
type StickerKind string

const (
	StickerKindEmote   StickerKind = "emote"
	StickerKindSticker StickerKind = "sticker"
)

// StickerPackStatus 包生命周期（软删 / 全局 ban / 硬删）。
type StickerPackStatus string

const (
	StickerPackActive             StickerPackStatus = "active"
	StickerPackSoftDeleted        StickerPackStatus = "soft_deleted"
	StickerPackSoftDeletedExpired StickerPackStatus = "soft_deleted_expired"
	StickerPackGloballyBanned     StickerPackStatus = "globally_banned"
	StickerPackPurged             StickerPackStatus = "purged"
)

// StickerItemStatus 条目状态。
type StickerItemStatus string

const (
	StickerItemActive StickerItemStatus = "active"
	StickerItemPurged StickerItemStatus = "purged"
)

// UserPackLibraryStatus 整包引用库状态（随作者软删软隐藏）。
type UserPackLibraryStatus string

const (
	UserPackLibraryActive UserPackLibraryStatus = "active"
	UserPackLibraryHidden UserPackLibraryStatus = "hidden"
)

// StickerPack 贴图包（账号级或服独属）。
type StickerPack struct {
	ID              int64              `gorm:"primaryKey;autoIncrement:false" json:"id,string"`
	OwnerUserID     uuid.UUID          `gorm:"type:uuid;not null;index:idx_sticker_pack_owner" json:"owner_user_id"`
	Scope           StickerPackScope   `gorm:"size:16;not null;index:idx_sticker_pack_scope" json:"scope"`
	GuildID         *uuid.UUID         `gorm:"type:uuid;index:idx_sticker_pack_guild" json:"guild_id,omitempty"`
	Kind            StickerKind        `gorm:"size:16;not null" json:"kind"`
	Name        string `gorm:"size:100;not null" json:"name"`
	Description string `gorm:"type:text;not null;default:''" json:"description,omitempty"`
	// CoverItemID 可选：指定包内某条目作为封面（无自定义上传封面时生效）。
	CoverItemID *int64 `json:"cover_item_id,string,omitempty"`
	// CoverAssetID 可选：用户上传的独立封面图（sticker_assets）；优先于 CoverItemID 与首条。
	CoverAssetID    *int64            `json:"cover_asset_id,string,omitempty"`
	AllowBrowseFull bool              `gorm:"not null;default:true" json:"allow_browse_full"`
	Status          StickerPackStatus  `gorm:"size:32;not null;default:'active';index:idx_sticker_pack_status" json:"status"`
	SoftDeletedAt   *time.Time         `json:"soft_deleted_at,omitempty"`
	RestoreDeadline *time.Time         `json:"restore_deadline,omitempty"`
	CreatedAt       time.Time          `json:"created_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
}

// StickerAsset 内容去重资产（A1）：同 content_hash 全局共享 blob。
type StickerAsset struct {
	ID          int64     `gorm:"primaryKey;autoIncrement:false" json:"id,string"`
	ContentHash string    `gorm:"size:64;not null;uniqueIndex:idx_sticker_asset_hash" json:"content_hash"`
	StorageKey  string    `gorm:"size:255;not null" json:"-"`
	MIME        string    `gorm:"size:64;not null" json:"mime"`
	SizeBytes   int64     `gorm:"not null" json:"size_bytes"`
	Width       int       `gorm:"not null" json:"width"`
	Height      int       `gorm:"not null" json:"height"`
	Animated    bool      `gorm:"not null;default:false" json:"animated"`
	RefCount    int       `gorm:"not null;default:0" json:"ref_count"`
	CreatedAt   time.Time `json:"created_at"`
}

// StickerItem 包内表情项。
type StickerItem struct {
	ID           int64             `gorm:"primaryKey;autoIncrement:false" json:"id,string"`
	PackID       int64             `gorm:"not null;index:idx_sticker_item_pack;index:idx_sticker_item_pack_sort,priority:1" json:"pack_id,string"`
	Kind         StickerKind       `gorm:"size:16;not null" json:"kind"`
	Name         string            `gorm:"size:100;not null;default:''" json:"name,omitempty"`
	ContentHash  string            `gorm:"size:64;not null;index:idx_sticker_item_hash" json:"content_hash"`
	Mark         string            `gorm:"size:32;not null;index:idx_sticker_item_mark" json:"mark"`
	AssetID      int64             `gorm:"not null;index:idx_sticker_item_asset" json:"asset_id,string"`
	Width        int               `gorm:"not null" json:"width"`
	Height       int               `gorm:"not null" json:"height"`
	Animated     bool              `gorm:"not null;default:false" json:"animated"`
	SourceItemID *int64            `json:"source_item_id,string,omitempty"`
	SourcePackID *int64            `json:"source_pack_id,string,omitempty"`
	// SortOrder 同 pack 内可重复；列表以 sort_order, id 稳定排序。
	// 不可 uniqueIndex：否则第二张默认 sort_order=0 会 DATABASE_ERROR。
	SortOrder int               `gorm:"not null;default:0;index:idx_sticker_item_pack_sort,priority:2" json:"sort_order"`
	Status    StickerItemStatus `gorm:"size:16;not null;default:'active'" json:"status"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// UserPackLibrary 用户整包引用库（Install = 引用，随原包变化）。
type UserPackLibrary struct {
	UserID      uuid.UUID             `gorm:"type:uuid;primaryKey" json:"user_id"`
	PackID      int64                 `gorm:"primaryKey;index:idx_user_pack_library_pack" json:"pack_id,string"`
	Status      UserPackLibraryStatus `gorm:"size:16;not null;default:'active'" json:"status"`
	InstalledAt time.Time             `gorm:"not null" json:"installed_at"`
	SortOrder   int                   `gorm:"not null;default:0" json:"sort_order"`
}

// GuildPackBan 服级封禁贴图包（不改 pack.status）。
type GuildPackBan struct {
	GuildID   uuid.UUID  `gorm:"type:uuid;primaryKey" json:"guild_id"`
	PackID    int64      `gorm:"primaryKey;index:idx_guild_pack_ban_pack" json:"pack_id,string"`
	BannedBy  uuid.UUID  `gorm:"type:uuid;not null" json:"banned_by"`
	Reason    string     `gorm:"size:500;not null;default:''" json:"reason,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// StickerQuotaOverride 用户级配额覆盖（后台可调；0 表示沿用全局默认）。
type StickerQuotaOverride struct {
	UserID       uuid.UUID `gorm:"type:uuid;primaryKey" json:"user_id"`
	MaxPacks     int       `gorm:"not null;default:0" json:"max_packs"`
	MaxItemsPack int       `gorm:"not null;default:0" json:"max_items_per_pack"`
	UpdatedAt    time.Time `json:"updated_at"`
	UpdatedBy    uuid.UUID `gorm:"type:uuid;not null" json:"updated_by"`
}

func init() {
	Register(
		&StickerPack{},
		&StickerAsset{},
		&StickerItem{},
		&UserPackLibrary{},
		&GuildPackBan{},
		&StickerQuotaOverride{},
	)
}
