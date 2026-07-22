package sticker

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm"
)

// customEmoteRe 匹配正文内嵌小表情节点 <e:item_id:mark>
var customEmoteRe = regexp.MustCompile(`<e:(\d+):([a-zA-Z0-9_]+)>`)

// ExtractCustomEmoteItemIDs 从消息正文提取自定义小表情 item_id（去重保序）。
func ExtractCustomEmoteItemIDs(content string) []int64 {
	matches := customEmoteRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(matches))
	ids := make([]int64, 0, len(matches))
	for _, m := range matches {
		id, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

// ParseReactionKey 解析反应路径键：unicode emoji 或 item:{item_id}。
// 返回 (itemID, isCustom, unicodeOrKey)。
func ParseReactionKey(emoji string) (itemID int64, isCustom bool, key string) {
	emoji = strings.TrimSpace(emoji)
	if strings.HasPrefix(emoji, ReactionItemPrefix) {
		raw := strings.TrimPrefix(emoji, ReactionItemPrefix)
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return 0, false, emoji
		}
		return id, true, ReactionItemPrefix + strconv.FormatInt(id, 10)
	}
	return 0, false, emoji
}

// ReactionKeyForItem 生成自定义反应的路径键。
func ReactionKeyForItem(itemID int64) string {
	return ReactionItemPrefix + strconv.FormatInt(itemID, 10)
}

// AvailableItem 用户在指定上下文中可发送的条目（含包元数据）。
type AvailableItem struct {
	Item model.StickerItem
	Pack model.StickerPack
}

// ItemAvailableForSend 校验 item 是否属于用户可用集合（docs 17 §5.1 L.1）。
// guildID 为 uuid.Nil 表示私信上下文：仅 account 包可用。
func ItemAvailableForSend(db *gorm.DB, userID, guildID uuid.UUID, itemID int64) (AvailableItem, error) {
	var item model.StickerItem
	if err := db.First(&item, "id = ? AND status = ?", itemID, model.StickerItemActive).Error; err != nil {
		return AvailableItem{}, errNotAvailable
	}
	var pack model.StickerPack
	if err := db.First(&pack, "id = ?", item.PackID).Error; err != nil {
		return AvailableItem{}, errNotAvailable
	}
	// 懒升迁过期软删
	pack = refreshSoftDeleteStatus(db, pack)
	if pack.Status != model.StickerPackActive {
		return AvailableItem{}, errNotAvailable
	}
	if pack.Kind != item.Kind {
		return AvailableItem{}, errNotAvailable
	}

	// 自建 active 包
	if pack.OwnerUserID == userID {
		if !scopeAllowsContext(pack, guildID) {
			return AvailableItem{}, errNotAvailable
		}
		if guildBanned(db, guildID, pack.ID) {
			return AvailableItem{}, errNotAvailable
		}
		return AvailableItem{Item: item, Pack: pack}, nil
	}

	// Install 引用：library active + pack active + scope + 未 ban
	var lib model.UserPackLibrary
	err := db.First(&lib, "user_id = ? AND pack_id = ? AND status = ?",
		userID, pack.ID, model.UserPackLibraryActive).Error
	if err != nil {
		return AvailableItem{}, errNotAvailable
	}
	if !scopeAllowsContext(pack, guildID) {
		return AvailableItem{}, errNotAvailable
	}
	if guildBanned(db, guildID, pack.ID) {
		return AvailableItem{}, errNotAvailable
	}
	return AvailableItem{Item: item, Pack: pack}, nil
}

// ItemResolvableForReaction 反应鉴权（R.5）：未 purged 即可展示与追加；不要求在库。
func ItemResolvableForReaction(db *gorm.DB, itemID int64) (model.StickerItem, model.StickerAsset, error) {
	var item model.StickerItem
	if err := db.First(&item, "id = ?", itemID).Error; err != nil {
		return item, model.StickerAsset{}, err
	}
	if item.Status == model.StickerItemPurged {
		// 仍允许计数；资源可能不可解析——返回 item 与空 asset 由调用方决定
		return item, model.StickerAsset{}, nil
	}
	var asset model.StickerAsset
	_ = db.First(&asset, "id = ?", item.AssetID).Error
	return item, asset, nil
}

// ResolveItemRef 组装消息载荷快照（发送成功后写入）。
func ResolveItemRef(db *gorm.DB, itemID int64) (MessageStickerRef, error) {
	var item model.StickerItem
	if err := db.First(&item, "id = ?", itemID).Error; err != nil {
		return MessageStickerRef{}, err
	}
	var asset model.StickerAsset
	_ = db.Select("storage_key").First(&asset, "id = ?", item.AssetID).Error
	return refFromItem(item, asset.StorageKey), nil
}

func scopeAllowsContext(pack model.StickerPack, guildID uuid.UUID) bool {
	switch pack.Scope {
	case model.StickerScopeAccount:
		return true // P.8 跨服
	case model.StickerScopeGuild:
		if guildID == uuid.Nil || pack.GuildID == nil {
			return false
		}
		return *pack.GuildID == guildID // P.9
	default:
		return false
	}
}

func guildBanned(db *gorm.DB, guildID uuid.UUID, packID int64) bool {
	if guildID == uuid.Nil {
		return false
	}
	var n int64
	db.Model(&model.GuildPackBan{}).Where("guild_id = ? AND pack_id = ?", guildID, packID).Count(&n)
	return n > 0
}

// refreshSoftDeleteStatus 软删满 180 天懒升迁为 soft_deleted_expired。
func refreshSoftDeleteStatus(db *gorm.DB, pack model.StickerPack) model.StickerPack {
	if pack.Status != model.StickerPackSoftDeleted {
		return pack
	}
	if pack.RestoreDeadline == nil || !time.Now().UTC().After(*pack.RestoreDeadline) {
		return pack
	}
	_ = db.Model(&model.StickerPack{}).Where("id = ? AND status = ?", pack.ID, model.StickerPackSoftDeleted).
		Update("status", model.StickerPackSoftDeletedExpired).Error
	pack.Status = model.StickerPackSoftDeletedExpired
	return pack
}

// listAvailablePackIDs 返回用户在上下文中可用的 pack id 集合。
func listAvailablePackIDs(db *gorm.DB, userID, guildID uuid.UUID) ([]int64, error) {
	// 自建 active
	var owned []model.StickerPack
	q := db.Where("owner_user_id = ? AND status = ?", userID, model.StickerPackActive)
	if err := q.Find(&owned).Error; err != nil {
		return nil, err
	}

	// Install active
	var libs []model.UserPackLibrary
	if err := db.Where("user_id = ? AND status = ?", userID, model.UserPackLibraryActive).Find(&libs).Error; err != nil {
		return nil, err
	}
	libIDs := make([]int64, 0, len(libs))
	for _, l := range libs {
		libIDs = append(libIDs, l.PackID)
	}
	var installed []model.StickerPack
	if len(libIDs) > 0 {
		if err := db.Where("id IN ? AND status = ?", libIDs, model.StickerPackActive).Find(&installed).Error; err != nil {
			return nil, err
		}
	}

	// 合并 + scope + ban
	var banned map[int64]struct{}
	if guildID != uuid.Nil {
		var bans []model.GuildPackBan
		_ = db.Where("guild_id = ?", guildID).Find(&bans).Error
		banned = make(map[int64]struct{}, len(bans))
		for _, b := range bans {
			banned[b.PackID] = struct{}{}
		}
	}

	seen := map[int64]struct{}{}
	out := make([]int64, 0, len(owned)+len(installed))
	add := func(p model.StickerPack) {
		if _, ok := seen[p.ID]; ok {
			return
		}
		if !scopeAllowsContext(p, guildID) {
			return
		}
		if banned != nil {
			if _, ok := banned[p.ID]; ok {
				return
			}
		}
		seen[p.ID] = struct{}{}
		out = append(out, p.ID)
	}
	for _, p := range owned {
		add(p)
	}
	for _, p := range installed {
		// 自己装的自己的包也算；已在 owned 去重
		add(p)
	}
	return out, nil
}

// canInstall 是否允许 Install（L.5）。
func canInstall(db *gorm.DB, pack model.StickerPack, viewerGuildID uuid.UUID) error {
	pack = refreshSoftDeleteStatus(db, pack)
	if pack.Status != model.StickerPackActive {
		return errCannotInstall
	}
	if !pack.AllowBrowseFull {
		return errCannotInstall
	}
	if pack.Scope == model.StickerScopeGuild {
		if pack.GuildID == nil || viewerGuildID == uuid.Nil || *pack.GuildID != viewerGuildID {
			return errCannotInstall
		}
	}
	if guildBanned(db, viewerGuildID, pack.ID) {
		return errCannotInstall
	}
	return nil
}

// canCopy 是否允许单条 Copy（P.5/P.6 B3）。
func canCopy(db *gorm.DB, pack model.StickerPack) error {
	pack = refreshSoftDeleteStatus(db, pack)
	if pack.Status != model.StickerPackActive {
		return errCannotCopy
	}
	if pack.Scope == model.StickerScopeGuild {
		return errCannotCopy // B3
	}
	// account 包：allow_browse_full=false 仍可 Copy；=true 也可
	return nil
}

// errIs 便于 handler 映射错误码。
func errIs(err, target error) bool { return errors.Is(err, target) }
