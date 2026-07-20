package model

import (
	"time"

	"github.com/google/uuid"
)

// ================= 密钥/连接信息同步（keysync 专项）=================

// SyncVault 用户跨端同步保险库：客户端把「服务器连接信息 + 各后端登录凭据」
// 用本地派生密钥加密后（零知识：服务端只存密文，无法解密）存于此，
// 多端登录同一账号即可实时同步。以 UserID 为主键，一账号一份。
//
// 并发控制：Version 单调递增，PUT 需带上一次读到的 Version 做乐观锁（不匹配 409）。
type SyncVault struct {
	UserID uuid.UUID `gorm:"type:uuid;primaryKey" json:"user_id"`
	// Ciphertext 客户端加密后的密文（Base64）；服务端不解析、不解密。
	Ciphertext string `gorm:"type:text;not null;default:''" json:"ciphertext"`
	// Nonce / KDFSalt 客户端加密参数（明文存储，供各端用同一口令派生密钥）。
	Nonce   string `gorm:"size:128;not null;default:''" json:"nonce"`
	KDFSalt string `gorm:"size:128;not null;default:''" json:"kdf_salt"`
	// Algo 加密算法标识（如 "xchacha20poly1305-argon2id"），供客户端识别。
	Algo string `gorm:"size:64;not null;default:''" json:"algo"`
	// Version 乐观锁版本；每次成功 PUT +1，跨端据此判断是否需要拉取更新。
	Version int64 `gorm:"not null;default:0" json:"version"`
	// DeviceID 最近一次写入的设备标识（客户端自报，便于展示「来自哪个设备」）。
	DeviceID  string    `gorm:"size:64;not null;default:''" json:"device_id"`
	UpdatedAt time.Time `json:"updated_at"`
	CreatedAt time.Time `json:"created_at"`
}

func init() { Register(&SyncVault{}) }
