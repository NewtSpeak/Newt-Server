package model

import "time"

// PlatformSetting 平台级键值配置（platformadmin 专项）：目前用于用户端注册开关等
// 可在控制台动态修改的全局开关；无对应行时各读取方回退到环境变量/内置默认值。
type PlatformSetting struct {
	Key       string    `gorm:"size:64;primaryKey" json:"key"`
	Value     string    `gorm:"size:255;not null;default:''" json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PlatformSettingClientSignup 用户端开放注册开关（"true"/"false"）。
const PlatformSettingClientSignup = "client_signup_enabled"

func init() { Register(&PlatformSetting{}) }
