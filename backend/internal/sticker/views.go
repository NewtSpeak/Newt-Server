package sticker

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
)

// packView 包对外投影。
type packView struct {
	ID              string                  `json:"id"`
	OwnerUserID     uuid.UUID               `json:"owner_user_id"`
	Scope           model.StickerPackScope  `json:"scope"`
	GuildID         *uuid.UUID              `json:"guild_id,omitempty"`
	Kind            model.StickerKind       `json:"kind"`
	Name            string                  `json:"name"`
	Description     string                  `json:"description,omitempty"`
	CoverItemID     *string                 `json:"cover_item_id,omitempty"`
	CoverAssetID    *string                 `json:"cover_asset_id,omitempty"`
	// CoverURL 解析后的封面图：自定义上传 > 指定条目 > 包内首条 active。
	CoverURL        string                  `json:"cover_url,omitempty"`
	// CoverCustom 是否为用户上传的独立封面（true 时可 DELETE cover 清除）。
	CoverCustom     bool                    `json:"cover_custom"`
	AllowBrowseFull bool                    `json:"allow_browse_full"`
	Status          model.StickerPackStatus `json:"status"`
	SoftDeletedAt   *time.Time              `json:"soft_deleted_at,omitempty"`
	RestoreDeadline *time.Time              `json:"restore_deadline,omitempty"`
	ItemCount       int                     `json:"item_count,omitempty"`
	Items           []itemView              `json:"items,omitempty"`
	CreatedAt       time.Time               `json:"created_at"`
	UpdatedAt       time.Time               `json:"updated_at"`
}

// itemView 条目对外投影（含资产 URL）。
type itemView struct {
	ID           string                 `json:"id"`
	PackID       string                 `json:"pack_id"`
	Kind         model.StickerKind      `json:"kind"`
	// Name 展示名：空时用 mark 回填，选择器下方始终有可读文案。
	Name         string                  `json:"name"`
	ContentHash  string                  `json:"content_hash"`
	Mark         string                  `json:"mark"`
	AssetID      string                 `json:"asset_id"`
	AssetURL     string                 `json:"asset_url"`
	Width        int                    `json:"width"`
	Height       int                    `json:"height"`
	Animated     bool                   `json:"animated"`
	SourceItemID *string                `json:"source_item_id,omitempty"`
	SourcePackID *string                `json:"source_pack_id,omitempty"`
	SortOrder    int                    `json:"sort_order"`
	Status       model.StickerItemStatus `json:"status"`
	CreatedAt    time.Time              `json:"created_at"`
	UpdatedAt    time.Time              `json:"updated_at"`
}

// libraryEntryView 贴图库条目。
type libraryEntryView struct {
	PackID      string                       `json:"pack_id"`
	Status      model.UserPackLibraryStatus  `json:"status"`
	InstalledAt time.Time                    `json:"installed_at"`
	SortOrder   int                          `json:"sort_order"`
	Pack        *packView                    `json:"pack,omitempty"`
}

// MessageStickerRef 消息/反应中的贴图引用快照（导出给 message 包）。
type MessageStickerRef struct {
	ItemID   string            `json:"item_id"`
	PackID   string            `json:"pack_id"`
	Mark     string            `json:"mark"`
	Kind     model.StickerKind `json:"kind,omitempty"`
	Animated bool              `json:"animated,omitempty"`
	AssetURL string            `json:"asset_url,omitempty"`
	Width    int               `json:"width,omitempty"`
	Height   int               `json:"height,omitempty"`
}

func toPackView(p model.StickerPack, itemCount int, items []itemView) packView {
	return toPackViewWithCover(p, itemCount, items, "", false)
}

func toPackViewWithCover(p model.StickerPack, itemCount int, items []itemView, coverURL string, coverCustom bool) packView {
	v := packView{
		ID:              strID(p.ID),
		OwnerUserID:     p.OwnerUserID,
		Scope:           p.Scope,
		GuildID:         p.GuildID,
		Kind:            p.Kind,
		Name:            p.Name,
		Description:     p.Description,
		CoverURL:        coverURL,
		CoverCustom:     coverCustom,
		AllowBrowseFull: p.AllowBrowseFull,
		Status:          p.Status,
		SoftDeletedAt:   p.SoftDeletedAt,
		RestoreDeadline: p.RestoreDeadline,
		ItemCount:       itemCount,
		Items:           items,
		CreatedAt:       p.CreatedAt,
		UpdatedAt:       p.UpdatedAt,
	}
	if p.CoverItemID != nil {
		s := strID(*p.CoverItemID)
		v.CoverItemID = &s
	}
	if p.CoverAssetID != nil {
		s := strID(*p.CoverAssetID)
		v.CoverAssetID = &s
	}
	return v
}

func toItemView(item model.StickerItem, assetURL string) itemView {
	displayName := strings.TrimSpace(item.Name)
	if displayName == "" {
		displayName = item.Mark
	}
	v := itemView{
		ID:          strID(item.ID),
		PackID:      strID(item.PackID),
		Kind:        item.Kind,
		Name:        displayName,
		ContentHash: item.ContentHash,
		Mark:        item.Mark,
		AssetID:     strID(item.AssetID),
		AssetURL:    assetURL,
		Width:       item.Width,
		Height:      item.Height,
		Animated:    item.Animated,
		SortOrder:   item.SortOrder,
		Status:      item.Status,
		CreatedAt:   item.CreatedAt,
		UpdatedAt:   item.UpdatedAt,
	}
	if item.SourceItemID != nil {
		s := strID(*item.SourceItemID)
		v.SourceItemID = &s
	}
	if item.SourcePackID != nil {
		s := strID(*item.SourcePackID)
		v.SourcePackID = &s
	}
	return v
}

func assetURL(storageKey string) string {
	if storageKey == "" {
		return ""
	}
	// storage_key 即文件名（含扩展名）
	return publicAssetURLPrefix + storageKey
}

func refFromItem(item model.StickerItem, storageKey string) MessageStickerRef {
	return MessageStickerRef{
		ItemID:   strID(item.ID),
		PackID:   strID(item.PackID),
		Mark:     item.Mark,
		Kind:     item.Kind,
		Animated: item.Animated,
		AssetURL: assetURL(storageKey),
		Width:    item.Width,
		Height:   item.Height,
	}
}
