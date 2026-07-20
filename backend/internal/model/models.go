package model

import (
	"time"

	"github.com/google/uuid"
)

type User struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	Username     string    `gorm:"size:32;uniqueIndex;not null" json:"username"`
	Email        string    `gorm:"size:255;uniqueIndex;not null" json:"email"`
	PasswordHash string    `gorm:"not null" json:"-"`
	SystemAdmin  bool      `gorm:"not null;default:false" json:"system_admin"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type RefreshToken struct {
	ID        uuid.UUID  `gorm:"type:uuid;primaryKey"`
	UserID    uuid.UUID  `gorm:"type:uuid;not null;index"`
	TokenHash string     `gorm:"size:64;uniqueIndex;not null"`
	ExpiresAt time.Time  `gorm:"not null;index"`
	RevokedAt *time.Time `gorm:"index"`
	CreatedAt time.Time
}

type Guild struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	Name        string    `gorm:"size:100;not null" json:"name"`
	OwnerUserID uuid.UUID `gorm:"type:uuid;not null;index" json:"owner_user_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Member struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	GuildID   uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_member_guild_user" json:"guild_id"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_member_guild_user" json:"user_id"`
	Nickname  string    `gorm:"size:64" json:"nickname"`
	CreatedAt time.Time `json:"created_at"`
}

type Role struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	GuildID     uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_role_guild_name" json:"guild_id"`
	Name        string    `gorm:"size:100;not null;uniqueIndex:idx_role_guild_name" json:"name"`
	Permissions int64     `gorm:"not null;default:0" json:"permissions"`
	Position    int       `gorm:"not null;default:0" json:"position"`
	IsEveryone  bool      `gorm:"not null;default:false" json:"is_everyone"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type MemberRole struct {
	MemberID uuid.UUID `gorm:"type:uuid;primaryKey"`
	RoleID   uuid.UUID `gorm:"type:uuid;primaryKey"`
}

type ChannelType string

const (
	ChannelText  ChannelType = "TEXT"
	ChannelVoice ChannelType = "VOICE"
)

type Channel struct {
	ID        uuid.UUID   `gorm:"type:uuid;primaryKey" json:"id"`
	GuildID   uuid.UUID   `gorm:"type:uuid;not null;index" json:"guild_id"`
	Name      string      `gorm:"size:100;not null" json:"name"`
	Type      ChannelType `gorm:"size:16;not null" json:"type"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
}

type OverwriteType string

const (
	OverwriteRole   OverwriteType = "ROLE"
	OverwriteMember OverwriteType = "MEMBER"
)

type ChannelOverwrite struct {
	ID        uuid.UUID     `gorm:"type:uuid;primaryKey" json:"id"`
	ChannelID uuid.UUID     `gorm:"type:uuid;not null;uniqueIndex:idx_overwrite_target" json:"channel_id"`
	Type      OverwriteType `gorm:"size:16;not null;uniqueIndex:idx_overwrite_target" json:"type"`
	TargetID  uuid.UUID     `gorm:"type:uuid;not null;uniqueIndex:idx_overwrite_target" json:"target_id"`
	Allow     int64         `gorm:"not null;default:0" json:"allow"`
	Deny      int64         `gorm:"not null;default:0" json:"deny"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

func Models() []any {
	return []any{&User{}, &RefreshToken{}, &Guild{}, &Member{}, &Role{}, &MemberRole{}, &Channel{}, &ChannelOverwrite{}}
}
