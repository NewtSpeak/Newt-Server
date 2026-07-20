package userapi

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 用户设置同步（docs 16 §7-1）：服务端存储不透明 JSON 文档（user_settings.data jsonb），
// 不校验业务字段（通知偏好 global/per-guild/per-channel 三层结构等由客户端约定），
// 仅做 JSON 对象合法性与大小上限校验。
//
// PATCH 合并语义（按 top-level key 合并）：请求体须为 JSON 对象；其每个顶层 key
// 整体替换文档中的同名 key（不做深层合并），值为 null 表示删除该 key。
// 客户端要更新通知偏好中的单个频道覆盖时，应提交整个 "notifications" 顶层对象。

const maxSettingsBytes = 64 << 10 // 合并后文档上限 64KB

// getSettings GET /users/@me/settings：读取设置文档（从未写入过时返回空对象）。
func (h *api) getSettings(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	var stored model.UserSettings
	err := h.deps.DB.First(&stored, "user_id = ?", user.ID).Error
	if err != nil {
		stored = model.UserSettings{UserID: user.ID, Data: "{}", UpdatedAt: time.Time{}}
	}
	c.JSON(http.StatusOK, gin.H{"settings": json.RawMessage(stored.Data), "updated_at": stored.UpdatedAt})
}

// patchSettings PATCH /users/@me/settings：按 top-level key 合并（见文件头注释），
// 成功后向本人全部端定向发 USER_SETTINGS_UPDATE（载荷为合并后的全量文档）。
func (h *api) patchSettings(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxSettingsBytes+1))
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "读取请求体失败")
		return
	}
	if int64(len(body)) > maxSettingsBytes {
		fail(c, http.StatusRequestEntityTooLarge, "SETTINGS_TOO_LARGE", "设置文档超过 64KB 上限")
		return
	}
	patch, ok := decodeObject(body)
	if !ok {
		fail(c, http.StatusBadRequest, "INVALID_SETTINGS", "请求体必须为 JSON 对象")
		return
	}

	var merged []byte
	err = h.deps.DB.Transaction(func(tx *gorm.DB) error {
		var stored model.UserSettings
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&stored, "user_id = ?", user.ID).Error; err != nil {
			stored = model.UserSettings{UserID: user.ID, Data: "{}"}
		}
		document, ok := decodeObject([]byte(stored.Data))
		if !ok {
			document = map[string]json.RawMessage{}
		}
		for key, value := range patch {
			if string(value) == "null" {
				delete(document, key)
				continue
			}
			document[key] = value
		}
		encoded, err := json.Marshal(document)
		if err != nil {
			return err
		}
		if len(encoded) > maxSettingsBytes {
			return errSettingsTooLarge
		}
		merged = encoded
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"data", "updated_at"}),
		}).Create(&model.UserSettings{UserID: user.ID, Data: string(encoded), UpdatedAt: time.Now().UTC()}).Error
	})
	if err == errSettingsTooLarge {
		fail(c, http.StatusRequestEntityTooLarge, "SETTINGS_TOO_LARGE", "合并后设置文档超过 64KB 上限")
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存设置失败")
		return
	}
	if h.deps.Bus != nil {
		h.deps.Bus.Publish(eventbus.Event{
			Type:    eventbus.EventUserSettingsUpdate,
			UserIDs: []uuid.UUID{user.ID},
			Payload: eventbus.NewUserSettingsUpdatePayload(merged),
		})
	}
	c.JSON(http.StatusOK, gin.H{"settings": json.RawMessage(merged), "updated_at": time.Now().UTC()})
}

// putSettings PUT /users/@me/settings：整体替换设置文档（docs 16 §7-1 建议形态之一）。
// 请求体即完整新文档（JSON 对象，≤64KB），服务端不解释内容；成功 204，
// 并向本人全部端定向发 USER_SETTINGS_UPDATE（载荷为新全量文档，其他端整体替换本地副本）。
func (h *api) putSettings(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxSettingsBytes+1))
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "读取请求体失败")
		return
	}
	if int64(len(body)) > maxSettingsBytes {
		fail(c, http.StatusRequestEntityTooLarge, "SETTINGS_TOO_LARGE", "设置文档超过 64KB 上限")
		return
	}
	document, ok := decodeObject(body)
	if !ok {
		fail(c, http.StatusBadRequest, "INVALID_SETTINGS", "请求体必须为 JSON 对象")
		return
	}
	// 重编码归一化（去除多余空白、稳定存储形态），也顺带丢弃重复 key 等边角输入。
	encoded, err := json.Marshal(document)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_SETTINGS", "设置文档无法编码")
		return
	}
	err = h.deps.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"data", "updated_at"}),
	}).Create(&model.UserSettings{UserID: user.ID, Data: string(encoded), UpdatedAt: time.Now().UTC()}).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存设置失败")
		return
	}
	if h.deps.Bus != nil {
		h.deps.Bus.Publish(eventbus.Event{
			Type:    eventbus.EventUserSettingsUpdate,
			UserIDs: []uuid.UUID{user.ID},
			Payload: eventbus.NewUserSettingsUpdatePayload(encoded),
		})
	}
	c.Status(http.StatusNoContent)
}

var errSettingsTooLarge = &settingsTooLargeError{}

type settingsTooLargeError struct{}

func (*settingsTooLargeError) Error() string { return "settings too large" }

// decodeObject 严格解析 JSON 对象（数组/标量/null/空体均判非法）。
func decodeObject(raw []byte) (map[string]json.RawMessage, bool) {
	var result map[string]json.RawMessage
	if err := json.Unmarshal(raw, &result); err != nil || result == nil {
		return nil, false
	}
	return result, true
}
