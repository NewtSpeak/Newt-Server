package restriction

import (
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/audit"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
	"gorm.io/gorm"
)

// cacheTTL 进程内生效限制缓存的时长：Mask 挂在权限计算主链路上，
// 短 TTL 兜住热点查询；创建/解除/过期时立即失效。
const cacheTTL = 5 * time.Second

type cacheKey struct {
	guildID uuid.UUID
	userID  uuid.UUID
}

type cacheEntry struct {
	restrictions []model.Restriction
	loadedAt     time.Time
}

// service Service 接口的数据库实现（docs 12 §6）。
type service struct {
	db  *gorm.DB
	bus *eventbus.Bus

	mu    sync.Mutex
	cache map[cacheKey]cacheEntry
}

// impl 包内单例：Register 时赋值；LiftAllForUser 等跨模块入口借此复用缓存失效。
var impl *service

func newService(db *gorm.DB, bus *eventbus.Bus) *service {
	return &service{db: db, bus: bus, cache: make(map[cacheKey]cacheEntry)}
}

// activeRestrictions 读取某用户在某服的生效限制（带短 TTL 缓存 + 惰性过期过滤）。
func (s *service) activeRestrictions(userID, guildID uuid.UUID, now time.Time) []model.Restriction {
	key := cacheKey{guildID: guildID, userID: userID}
	s.mu.Lock()
	entry, ok := s.cache[key]
	s.mu.Unlock()
	if ok && now.Sub(entry.loadedAt) < cacheTTL {
		return entry.restrictions
	}
	var rows []model.Restriction
	err := s.db.
		Where("guild_id = ? AND target_user_id = ? AND lifted_at IS NULL AND (expires_at IS NULL OR expires_at > ?)", guildID, userID, now).
		Find(&rows).Error
	if err != nil {
		// 查询失败时不收紧也不缓存（收紧是惩罚层，出错宁可放行也不能误伤，并记录日志排查）。
		log.Printf("restriction: 读取生效限制失败 guild=%s user=%s err=%v", guildID, userID, err)
		return nil
	}
	s.mu.Lock()
	s.cache[key] = cacheEntry{restrictions: rows, loadedAt: now}
	s.mu.Unlock()
	return rows
}

// invalidate 使某用户在某服的缓存立即失效（创建/修改/解除/过期时调用）。
func (s *service) invalidate(guildID, userID uuid.UUID) {
	s.mu.Lock()
	delete(s.cache, cacheKey{guildID: guildID, userID: userID})
	s.mu.Unlock()
}

// Mask 实现 Service：在 RBAC bits 上按生效限制只收紧（docs 12 AL.1）。
func (s *service) Mask(bits rbac.Permission, userID, guildID uuid.UUID, channel *model.Channel) rbac.Permission {
	now := time.Now().UTC()
	rows := s.activeRestrictions(userID, guildID, now)
	if len(rows) == 0 {
		return bits
	}
	return MaskWithRestrictions(bits, rows, channel, now)
}

// Denies 实现 Service：聚合某位置当前被禁维度的并集。
func (s *service) Denies(userID, guildID uuid.UUID, channelID *uuid.UUID, channelType model.ChannelType) DenyFlags {
	now := time.Now().UTC()
	rows := s.activeRestrictions(userID, guildID, now)
	if len(rows) == 0 {
		return DenyFlags{}
	}
	return DenyUnionFor(rows, channelID, channelType, now)
}

// publishChange 发布限制变化事件（docs 12 AL.5）：
//   - 当事人定向推送完整记录（含 reason，AM.1）；
//   - 服务器广播简表（不含 reason，AM.2）；
//   - 影响语音（listen/speak）时补发 InternalCapsDirty，语音模块据此重算 caps 或断开；
//   - 使当事人权限缓存失效。
func publishChange(db *gorm.DB, bus *eventbus.Bus, eventType string, r model.Restriction, now time.Time) {
	guildID := r.GuildID
	bus.Publish(eventbus.Event{
		Type:    eventType,
		GuildID: &guildID,
		UserIDs: []uuid.UUID{r.TargetUserID},
		Payload: viewOf(r, true, true, now),
	})
	bus.Publish(eventbus.Event{
		Type:    eventType,
		GuildID: &guildID,
		Payload: viewOf(r, false, false, now),
	})
	publishCapsDirty(db, bus, r)
	if impl != nil {
		impl.invalidate(r.GuildID, r.TargetUserID)
	}
}

// publishCapsDirty 限制涉及语音维度且当事人正在语音频道内时发布 InternalCapsDirty。
// 不在房内时进房校验走 Mask 主链路即可，无需重算。
func publishCapsDirty(db *gorm.DB, bus *eventbus.Bus, r model.Restriction) {
	if !r.DenyListenVoice && !r.DenySpeakVoice {
		return
	}
	var state model.VoiceState
	err := db.First(&state, "guild_id = ? AND user_id = ?", r.GuildID, r.TargetUserID).Error
	if err != nil || state.ChannelID == nil {
		return
	}
	// 单频道作用域且当事人不在该频道时，当前房间 caps 不受影响。
	if Scope(r.Scope) == ScopeVoiceChannel && r.ChannelID != nil && *r.ChannelID != *state.ChannelID {
		return
	}
	guildID, channelID := r.GuildID, *state.ChannelID
	bus.Publish(eventbus.Event{
		Type:      eventbus.InternalCapsDirty,
		GuildID:   &guildID,
		ChannelID: &channelID,
		UserIDs:   []uuid.UUID{r.TargetUserID},
		Payload: eventbus.CapsDirtyPayload{
			GuildID:   guildID.String(),
			ChannelID: channelID.String(),
			UserID:    r.TargetUserID.String(),
			Reason:    "restriction",
		},
	})
}

// LiftAllForUser 失活某用户在本服的全部生效限制（docs 12 AO.3：Ban 联动清理）。
// 供 moderation 模块在 Ban 成员时调用；返回失活条数。
func LiftAllForUser(db *gorm.DB, bus *eventbus.Bus, guildID, targetUserID, actorID uuid.UUID) (int, error) {
	now := time.Now().UTC()
	var active []model.Restriction
	err := db.Where("guild_id = ? AND target_user_id = ? AND lifted_at IS NULL AND (expires_at IS NULL OR expires_at > ?)", guildID, targetUserID, now).
		Find(&active).Error
	if err != nil {
		return 0, err
	}
	lifted := 0
	for i := range active {
		r := &active[i]
		result := db.Model(&model.Restriction{}).
			Where("id = ? AND lifted_at IS NULL", r.ID).
			Updates(map[string]any{"lifted_at": now, "lifted_by": actorID})
		if result.Error != nil || result.RowsAffected == 0 {
			continue
		}
		r.LiftedAt, r.LiftedBy = &now, &actorID
		publishChange(db, bus, eventbus.EventRestrictionLift, *r, now)
		lifted++
	}
	return lifted, nil
}

// expiryLoop 定时扫描过期限制（docs 12 AN.1，间隔 30s），配合查询侧惰性过滤。
func (s *service) expiryLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.sweepExpired(time.Now().UTC())
	}
}

// sweepExpired 找出已过期但尚未通知的记录：打过期标记、推 lift 事件、写审计（AN.2）。
func (s *service) sweepExpired(now time.Time) {
	var expired []model.Restriction
	err := s.db.
		Where("lifted_at IS NULL AND expired_notified_at IS NULL AND expires_at IS NOT NULL AND expires_at <= ?", now).
		Limit(500).Find(&expired).Error
	if err != nil {
		log.Printf("restriction: 过期扫描失败 err=%v", err)
		return
	}
	for i := range expired {
		r := &expired[i]
		result := s.db.Model(&model.Restriction{}).
			Where("id = ? AND expired_notified_at IS NULL", r.ID).
			Update("expired_notified_at", now)
		if result.Error != nil || result.RowsAffected == 0 {
			continue
		}
		notified := now
		r.ExpiredNotifiedAt = &notified
		publishChange(s.db, s.bus, eventbus.EventRestrictionLift, *r, now)
		guildID := r.GuildID
		audit.Log(s.db, audit.Entry{
			ActorType:  "auto",
			GuildID:    &guildID,
			Action:     "restriction.expire",
			TargetType: "restriction",
			TargetID:   r.ID.String(),
			Detail:     map[string]any{"target_user_id": r.TargetUserID, "scope": r.Scope, "expires_at": r.ExpiresAt},
		})
	}
}
