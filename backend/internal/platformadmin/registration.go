package platformadmin

import (
	"net/http"
	"os"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// envSignupFallback CLIENT_SIGNUP_ENABLED 环境变量解析（缺省/非法值 = true），
// 语义与 clientapi 启动期解析一致，作为 DB 无记录时的兜底。
func envSignupFallback() bool {
	value := os.Getenv("CLIENT_SIGNUP_ENABLED")
	if value == "" {
		return true
	}
	enabled, err := strconv.ParseBool(value)
	if err != nil {
		return true
	}
	return enabled
}

// ClientSignupEnabled 用户端注册开关的权威读取：DB（PlatformSetting）优先，
// 无记录时回退 fallback（来自 CLIENT_SIGNUP_ENABLED 环境变量的启动期解析）。
// 供 clientapi 在每次 signup 请求时调用，控制台修改即时生效、无需重启。
// db 未注入（纯逻辑单测）时同样回退 fallback。
func ClientSignupEnabled(db *gorm.DB, fallback bool) bool {
	if db == nil {
		return fallback
	}
	var setting model.PlatformSetting
	if err := db.First(&setting, "key = ?", model.PlatformSettingClientSignup).Error; err != nil {
		return fallback
	}
	enabled, err := strconv.ParseBool(setting.Value)
	if err != nil {
		return fallback
	}
	return enabled
}

// getRegistration GET /admin/registration：注册开关当前值与来源（db/env 默认）。
func (h *api) getRegistration(c *gin.Context) {
	var setting model.PlatformSetting
	source := "default"
	enabled := envSignupFallback()
	if err := h.deps.DB.First(&setting, "key = ?", model.PlatformSettingClientSignup).Error; err == nil {
		if parsed, err := strconv.ParseBool(setting.Value); err == nil {
			enabled, source = parsed, "db"
		}
	}
	c.JSON(http.StatusOK, gin.H{"signup_enabled": enabled, "source": source})
}

type registrationRequest struct {
	SignupEnabled *bool `json:"signup_enabled" binding:"required"`
}

// putRegistration PUT /admin/registration：写入注册开关（落库，即时生效）。
func (h *api) putRegistration(c *gin.Context) {
	var input registrationRequest
	if !bind(c, &input) {
		return
	}
	setting := model.PlatformSetting{
		Key:   model.PlatformSettingClientSignup,
		Value: strconv.FormatBool(*input.SignupEnabled),
	}
	err := h.deps.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&setting).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存注册开关失败")
		return
	}
	h.audit(c, "platform.registration_update", model.PlatformSettingClientSignup, map[string]any{
		"signup_enabled": *input.SignupEnabled,
	})
	c.JSON(http.StatusOK, gin.H{"signup_enabled": *input.SignupEnabled, "source": "db"})
}
