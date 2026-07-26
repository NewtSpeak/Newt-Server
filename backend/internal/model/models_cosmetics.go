package model

import (
	"time"

	"github.com/google/uuid"
)

// ================= 平台装扮商店（Cosmetics Store）=================
// 平台级独立装扮：品类可运行时扩展；单品/捆绑；标签筛选；积分兑换；
// 服务器货币→积分入口预留。装扮为账号全局持有与装备（非 per-guild）。

// CosmeticItemStatus 商品生命周期。
type CosmeticItemStatus string

const (
	CosmeticStatusDraft     CosmeticItemStatus = "draft"
	CosmeticStatusPublished CosmeticItemStatus = "published"
	CosmeticStatusArchived  CosmeticItemStatus = "archived"
)

// CosmeticInventorySource 库存来源。
type CosmeticInventorySource string

const (
	CosmeticSourceClaim      CosmeticInventorySource = "claim"
	CosmeticSourcePoints     CosmeticInventorySource = "points"
	CosmeticSourceBundle     CosmeticInventorySource = "bundle"
	CosmeticSourceAdminGrant CosmeticInventorySource = "admin_grant"
	CosmeticSourcePromo      CosmeticInventorySource = "promo"
)

// CosmeticOrderTarget 订单目标类型。
type CosmeticOrderTarget string

const (
	CosmeticTargetItem   CosmeticOrderTarget = "item"
	CosmeticTargetBundle CosmeticOrderTarget = "bundle"
)

// CosmeticOrderStatus 订单状态。
type CosmeticOrderStatus string

const (
	CosmeticOrderCompleted CosmeticOrderStatus = "completed"
	CosmeticOrderFailed    CosmeticOrderStatus = "failed"
)

// CosmeticCategory 可扩展品类注册表（schema 驱动资产槽与 payload）。
type CosmeticCategory struct {
	Key         string    `gorm:"size:64;primaryKey" json:"key"`
	Name        string    `gorm:"size:100;not null" json:"name"`
	Description string    `gorm:"size:500;not null;default:''" json:"description"`
	// Slot 装备槽；同槽位互斥（如 avatar_frame）。
	Slot string `gorm:"size:64;not null;index:idx_cosmetic_category_slot" json:"slot"`
	// SchemaJSON 资产槽 + payload 字段定义（jsonb）。
	SchemaJSON string    `gorm:"type:jsonb;not null;default:'{}'" json:"schema_json"`
	SortOrder  int       `gorm:"not null;default:0" json:"sort_order"`
	Enabled    bool      `gorm:"not null;default:true" json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// CosmeticAsset 内容去重资产（与 sticker_assets 同构）。
type CosmeticAsset struct {
	ID          int64     `gorm:"primaryKey;autoIncrement:false" json:"id,string"`
	ContentHash string    `gorm:"size:64;not null;uniqueIndex:idx_cosmetic_asset_hash" json:"content_hash"`
	StorageKey  string    `gorm:"size:255;not null" json:"-"`
	MIME        string    `gorm:"size:64;not null" json:"mime"`
	SizeBytes   int64     `gorm:"not null" json:"size_bytes"`
	Width       int       `gorm:"not null;default:0" json:"width"`
	Height      int       `gorm:"not null;default:0" json:"height"`
	DurationMs  int       `gorm:"not null;default:0" json:"duration_ms"`
	Animated    bool      `gorm:"not null;default:false" json:"animated"`
	RefCount    int       `gorm:"not null;default:0" json:"ref_count"`
	CreatedAt   time.Time `json:"created_at"`
}

// CosmeticItem 单品 SKU。
type CosmeticItem struct {
	ID             int64              `gorm:"primaryKey;autoIncrement:false" json:"id,string"`
	CategoryKey    string             `gorm:"size:64;not null;index:idx_cosmetic_item_category" json:"category_key"`
	Name           string             `gorm:"size:100;not null" json:"name"`
	Description    string             `gorm:"type:text;not null;default:''" json:"description"`
	PreviewAssetID *int64             `json:"preview_asset_id,string,omitempty"`
	// AssetsJSON 槽位 → asset_id 映射，如 {"primary":"123","compact":"456"}。
	AssetsJSON string `gorm:"type:jsonb;not null;default:'{}'" json:"assets_json"`
	// PayloadJSON 品类特有配置（motion / gradient / base_color 等）。
	PayloadJSON   string             `gorm:"type:jsonb;not null;default:'{}'" json:"payload_json"`
	PricePoints   int                `gorm:"not null;default:0" json:"price_points"`
	Status        CosmeticItemStatus `gorm:"size:16;not null;default:'draft';index:idx_cosmetic_item_status" json:"status"`
	SortOrder     int                `gorm:"not null;default:0" json:"sort_order"`
	AvailableFrom *time.Time         `json:"available_from,omitempty"`
	AvailableUntil *time.Time        `json:"available_until,omitempty"`
	CreatedBy     uuid.UUID          `gorm:"type:uuid;not null" json:"created_by"`
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
}

// CosmeticBundle 捆绑包。
type CosmeticBundle struct {
	ID             int64              `gorm:"primaryKey;autoIncrement:false" json:"id,string"`
	Name           string             `gorm:"size:100;not null" json:"name"`
	Description    string             `gorm:"type:text;not null;default:''" json:"description"`
	PreviewAssetID *int64             `json:"preview_asset_id,string,omitempty"`
	PricePoints    int                `gorm:"not null;default:0" json:"price_points"`
	Status         CosmeticItemStatus `gorm:"size:16;not null;default:'draft';index:idx_cosmetic_bundle_status" json:"status"`
	SortOrder      int                `gorm:"not null;default:0" json:"sort_order"`
	AvailableFrom  *time.Time         `json:"available_from,omitempty"`
	AvailableUntil *time.Time         `json:"available_until,omitempty"`
	CreatedBy      uuid.UUID          `gorm:"type:uuid;not null" json:"created_by"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
}

// CosmeticBundleItem 捆绑包内单品。
type CosmeticBundleItem struct {
	BundleID int64 `gorm:"primaryKey;autoIncrement:false" json:"bundle_id,string"`
	ItemID   int64 `gorm:"primaryKey;autoIncrement:false;index:idx_cosmetic_bundle_item" json:"item_id,string"`
}

// CosmeticTag 主题标签。
type CosmeticTag struct {
	ID        int64     `gorm:"primaryKey;autoIncrement:false" json:"id,string"`
	Key       string    `gorm:"size:64;not null;uniqueIndex:idx_cosmetic_tag_key" json:"key"`
	Name      string    `gorm:"size:100;not null" json:"name"`
	Color     string    `gorm:"size:32;not null;default:''" json:"color"`
	SortOrder int       `gorm:"not null;default:0" json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CosmeticItemTag 单品-标签。
type CosmeticItemTag struct {
	ItemID int64 `gorm:"primaryKey;autoIncrement:false" json:"item_id,string"`
	TagID  int64 `gorm:"primaryKey;autoIncrement:false;index:idx_cosmetic_item_tag" json:"tag_id,string"`
}

// CosmeticBundleTag 捆绑-标签。
type CosmeticBundleTag struct {
	BundleID int64 `gorm:"primaryKey;autoIncrement:false" json:"bundle_id,string"`
	TagID    int64 `gorm:"primaryKey;autoIncrement:false;index:idx_cosmetic_bundle_tag" json:"tag_id,string"`
}

// UserCosmeticInventory 用户装扮库存。
type UserCosmeticInventory struct {
	ID         int64                    `gorm:"primaryKey;autoIncrement:false" json:"id,string"`
	UserID     uuid.UUID                `gorm:"type:uuid;not null;uniqueIndex:idx_user_cosmetic_once;index:idx_user_cosmetic_user" json:"user_id"`
	ItemID     int64                    `gorm:"not null;uniqueIndex:idx_user_cosmetic_once;index:idx_user_cosmetic_item" json:"item_id,string"`
	Source     CosmeticInventorySource  `gorm:"size:32;not null" json:"source"`
	SourceRef  string                   `gorm:"size:128;not null;default:''" json:"source_ref"`
	ExpiresAt  *time.Time               `gorm:"index:idx_user_cosmetic_expires" json:"expires_at"`
	AcquiredAt time.Time                `gorm:"not null" json:"acquired_at"`
}

// UserCosmeticLoadout 用户当前装备（每槽一位）。
type UserCosmeticLoadout struct {
	UserID     uuid.UUID `gorm:"type:uuid;primaryKey" json:"user_id"`
	Slot       string    `gorm:"size:64;primaryKey" json:"slot"`
	ItemID     int64     `gorm:"not null;index:idx_user_cosmetic_loadout_item" json:"item_id,string"`
	EquippedAt time.Time `gorm:"not null" json:"equipped_at"`
}

// UserPoints 用户平台积分余额。
type UserPoints struct {
	UserID    uuid.UUID `gorm:"type:uuid;primaryKey" json:"user_id"`
	Balance   int64     `gorm:"not null;default:0" json:"balance"`
	UpdatedAt time.Time `json:"updated_at"`
}

// UserPointsLedger 积分流水。
type UserPointsLedger struct {
	ID           int64     `gorm:"primaryKey;autoIncrement:false" json:"id,string"`
	UserID       uuid.UUID `gorm:"type:uuid;not null;index:idx_points_ledger_user" json:"user_id"`
	Delta        int64     `gorm:"not null" json:"delta"`
	BalanceAfter int64     `gorm:"not null" json:"balance_after"`
	Reason       string    `gorm:"size:64;not null" json:"reason"`
	RefType      string    `gorm:"size:32;not null;default:''" json:"ref_type"`
	RefID        string    `gorm:"size:128;not null;default:''" json:"ref_id"`
	CreatedAt    time.Time `json:"created_at"`
}

// CosmeticOrder 兑换订单（积分购买记录）。
type CosmeticOrder struct {
	ID          int64                `gorm:"primaryKey;autoIncrement:false" json:"id,string"`
	UserID      uuid.UUID            `gorm:"type:uuid;not null;index:idx_cosmetic_order_user" json:"user_id"`
	TargetType  CosmeticOrderTarget  `gorm:"size:16;not null" json:"target_type"`
	TargetID    int64                `gorm:"not null" json:"target_id,string"`
	PricePoints int                  `gorm:"not null" json:"price_points"`
	Status      CosmeticOrderStatus  `gorm:"size:16;not null" json:"status"`
	CreatedAt   time.Time            `json:"created_at"`
}

// CosmeticCurrencyExchangeIntent 服务器货币→积分预留意图（本期不实现兑换逻辑）。
type CosmeticCurrencyExchangeIntent struct {
	ID             int64      `gorm:"primaryKey;autoIncrement:false" json:"id,string"`
	UserID         uuid.UUID  `gorm:"type:uuid;not null;index:idx_cosmetic_exchange_user" json:"user_id"`
	GuildID        *uuid.UUID `gorm:"type:uuid" json:"guild_id,omitempty"`
	CurrencyCode   string     `gorm:"size:32;not null;default:''" json:"currency_code"`
	CurrencyAmount int64      `gorm:"not null;default:0" json:"currency_amount"`
	PointsAmount   int64      `gorm:"not null;default:0" json:"points_amount"`
	Status         string     `gorm:"size:16;not null;default:'pending'" json:"status"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func init() {
	Register(
		&CosmeticCategory{},
		&CosmeticAsset{},
		&CosmeticItem{},
		&CosmeticBundle{},
		&CosmeticBundleItem{},
		&CosmeticTag{},
		&CosmeticItemTag{},
		&CosmeticBundleTag{},
		&UserCosmeticInventory{},
		&UserCosmeticLoadout{},
		&UserPoints{},
		&UserPointsLedger{},
		&CosmeticOrder{},
		&CosmeticCurrencyExchangeIntent{},
	)
}
