package gateway

import (
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/perms"
	"github.com/owlspeak/owl-server/backend/internal/security"
	"github.com/owlspeak/owl-server/backend/internal/snapshot"
	"github.com/owlspeak/owl-server/backend/internal/social"
	"gorm.io/gorm"
)

// authenticator 抽象 IDENTIFY 认证：返回用户信息与已加入 guild 列表（供 READY）。
type authenticator interface {
	Authenticate(token string) (model.User, []uuid.UUID, error)
}

// dbAuthenticator 生产实现：解析 access token（与 httpapi 同一 secret，结果一致）
// 并从数据库加载用户与成员关系。audience 决定接受哪个受众的 token
//（后台 WS 收 aud=admin，用户端 WS 收 aud=client，凭证互不相通）。
type dbAuthenticator struct {
	db       *gorm.DB
	tokens   *security.TokenManager
	audience string
}

func (a *dbAuthenticator) Authenticate(token string) (model.User, []uuid.UUID, error) {
	userID, err := a.tokens.ParseAccessTokenWithAudience(token, a.audience)
	if err != nil {
		return model.User{}, nil, err
	}
	var user model.User
	if err := a.db.First(&user, "id = ?", userID).Error; err != nil {
		return model.User{}, nil, errors.New("用户不存在")
	}
	var guildIDs []uuid.UUID
	// 系统所有者 READY 携带平台全部服务器快照（docs 04 FR-32）；普通用户仅成员关系。
	if user.SystemAdmin {
		err = a.db.Model(&model.Guild{}).Order("created_at DESC").Pluck("id", &guildIDs).Error
	} else {
		err = a.db.Model(&model.Member{}).Where("user_id = ?", user.ID).Pluck("guild_id", &guildIDs).Error
	}
	if err != nil {
		return model.User{}, nil, err
	}
	return user, guildIDs, nil
}

// dbDirectory 生产实现：成员列表与可见性判定均走 PostgreSQL（perms 统一权限计算）。
type dbDirectory struct {
	db *gorm.DB
}

func (d *dbDirectory) GuildMemberIDs(guildID uuid.UUID) ([]uuid.UUID, error) {
	var userIDs []uuid.UUID
	if err := d.db.Model(&model.Member{}).Where("guild_id = ?", guildID).Pluck("user_id", &userIDs).Error; err != nil {
		return nil, err
	}
	return userIDs, nil
}

func (d *dbDirectory) CanSeeChannel(user model.User, guildID, channelID uuid.UUID) bool {
	return perms.CanSeeChannel(d.db, user, guildID, channelID)
}

func (d *dbDirectory) CanAccessChannelContent(user model.User, guildID, channelID uuid.UUID) bool {
	return perms.CanAccessChannelContent(d.db, user, guildID, channelID)
}

func (d *dbDirectory) GuildSnapshots(user model.User, guildIDs []uuid.UUID) ([]snapshot.Guild, error) {
	return snapshot.BuildGuilds(d.db, user, guildIDs)
}

func (d *dbDirectory) ReadStates(userID uuid.UUID, channelIDs []uuid.UUID) ([]snapshot.ReadState, error) {
	return snapshot.BuildReadStates(d.db, userID, channelIDs)
}

func (d *dbDirectory) SocialSnapshot(userID uuid.UUID) (any, any, any, int64) {
	rels, privacy, privateChannels, unread := social.SnapshotForReady(d.db, userID)
	return rels, privacy, privateChannels, unread
}

// memberCache 对 GuildMemberIDs 做短 TTL 缓存（默认 30s）减少广播时的成员查询；
// 可见性判定不缓存（Restriction 等要求近实时收紧，docs 12 §6.3）。
type memberCache struct {
	inner directory
	ttl   time.Duration

	mu      sync.Mutex
	entries map[uuid.UUID]memberCacheEntry
}

type memberCacheEntry struct {
	userIDs   []uuid.UUID
	expiresAt time.Time
}

func newMemberCache(inner directory, ttl time.Duration) *memberCache {
	return &memberCache{inner: inner, ttl: ttl, entries: make(map[uuid.UUID]memberCacheEntry)}
}

func (c *memberCache) GuildMemberIDs(guildID uuid.UUID) ([]uuid.UUID, error) {
	now := time.Now()
	c.mu.Lock()
	entry, ok := c.entries[guildID]
	c.mu.Unlock()
	if ok && now.Before(entry.expiresAt) {
		return entry.userIDs, nil
	}
	userIDs, err := c.inner.GuildMemberIDs(guildID)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.entries[guildID] = memberCacheEntry{userIDs: userIDs, expiresAt: now.Add(c.ttl)}
	c.mu.Unlock()
	return userIDs, nil
}

func (c *memberCache) CanSeeChannel(user model.User, guildID, channelID uuid.UUID) bool {
	return c.inner.CanSeeChannel(user, guildID, channelID)
}

func (c *memberCache) CanAccessChannelContent(user model.User, guildID, channelID uuid.UUID) bool {
	return c.inner.CanAccessChannelContent(user, guildID, channelID)
}

func (c *memberCache) GuildSnapshots(user model.User, guildIDs []uuid.UUID) ([]snapshot.Guild, error) {
	return c.inner.GuildSnapshots(user, guildIDs)
}

func (c *memberCache) ReadStates(userID uuid.UUID, channelIDs []uuid.UUID) ([]snapshot.ReadState, error) {
	return c.inner.ReadStates(userID, channelIDs)
}

func (c *memberCache) SocialSnapshot(userID uuid.UUID) (any, any, any, int64) {
	return c.inner.SocialSnapshot(userID)
}
