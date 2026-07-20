// Package keysync 密钥/连接信息跨端同步服务（零知识）：
//   - 客户端把「服务器连接信息 + 各后端登录凭据」用本地口令派生密钥加密后上传；
//   - 服务端只存密文（无法解密），以 UserID 为主键，一账号一份 SyncVault；
//   - 多端登录同一账号时，PUT 成功后经 Gateway 定向下发 VAULT_UPDATE，实现实时同步；
//   - 乐观并发：Version 单调递增，PUT 需带 base_version，冲突返回 409 让客户端先合并。
//
// 挂载于用户端平面（/gapi/v1，aud=client）：同一 OwlSpeak 后端账号即同步单元。
package keysync

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type api struct {
	deps appdeps.Deps
}

// errVersionConflict 乐观锁版本不匹配（事务内哨兵错误）。
var errVersionConflict = errors.New("vault version conflict")

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func fail(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

// RegisterClient 挂载用户端密钥同步端点。
func RegisterClient(authed *gin.RouterGroup, deps appdeps.Deps) {
	h := &api{deps: deps}
	authed.GET("/users/@me/vault", h.getVault)
	authed.PUT("/users/@me/vault", h.putVault)
	authed.DELETE("/users/@me/vault", h.deleteVault)
}

// vaultView 对外视图（密文原样返回，服务端从不解析）。
type vaultView struct {
	Ciphertext string     `json:"ciphertext"`
	Nonce      string     `json:"nonce"`
	KDFSalt    string     `json:"kdf_salt"`
	Algo       string     `json:"algo"`
	Version    int64      `json:"version"`
	DeviceID   string     `json:"device_id"`
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
}

// getVault GET /users/@me/vault：拉取本账号的加密保险库（不存在则返回 version=0 空库）。
func (h *api) getVault(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	var vault model.SyncVault
	if err := h.deps.DB.First(&vault, "user_id = ?", user.ID).Error; err != nil {
		c.JSON(http.StatusOK, vaultView{Version: 0})
		return
	}
	updated := vault.UpdatedAt
	c.JSON(http.StatusOK, vaultView{
		Ciphertext: vault.Ciphertext, Nonce: vault.Nonce, KDFSalt: vault.KDFSalt,
		Algo: vault.Algo, Version: vault.Version, DeviceID: vault.DeviceID, UpdatedAt: &updated,
	})
}

type putVaultRequest struct {
	Ciphertext string `json:"ciphertext" binding:"required"`
	Nonce      string `json:"nonce" binding:"max=128"`
	KDFSalt    string `json:"kdf_salt" binding:"max=128"`
	Algo       string `json:"algo" binding:"max=64"`
	// BaseVersion 客户端本次编辑所基于的版本；服务端当前版本与之不符则 409。
	BaseVersion int64  `json:"base_version"`
	DeviceID    string `json:"device_id" binding:"max=64"`
}

// putVault PUT /users/@me/vault：写入加密保险库（乐观锁 + 版本自增 + 多端实时下发）。
func (h *api) putVault(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	var input putVaultRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if len(input.Ciphertext) > 4<<20 {
		fail(c, http.StatusBadRequest, "TOO_LARGE", "保险库密文超过 4MB 上限")
		return
	}
	now := time.Now().UTC()
	var saved model.SyncVault
	err := h.deps.DB.Transaction(func(tx *gorm.DB) error {
		var current model.SyncVault
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", user.ID).First(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 首次写入：base_version 必须为 0。
			if input.BaseVersion != 0 {
				return errVersionConflict
			}
			saved = model.SyncVault{
				UserID: user.ID, Ciphertext: input.Ciphertext, Nonce: input.Nonce,
				KDFSalt: input.KDFSalt, Algo: input.Algo, Version: 1,
				DeviceID: input.DeviceID, CreatedAt: now, UpdatedAt: now,
			}
			return tx.Create(&saved).Error
		}
		if err != nil {
			return err
		}
		if current.Version != input.BaseVersion {
			return errVersionConflict
		}
		current.Ciphertext, current.Nonce = input.Ciphertext, input.Nonce
		current.KDFSalt, current.Algo = input.KDFSalt, input.Algo
		current.Version, current.DeviceID, current.UpdatedAt = current.Version+1, input.DeviceID, now
		saved = current
		return tx.Save(&current).Error
	})
	if errors.Is(err, errVersionConflict) {
		// 冲突：返回当前服务端版本供客户端合并后重试。
		var current model.SyncVault
		_ = h.deps.DB.First(&current, "user_id = ?", user.ID).Error
		c.Header("X-Vault-Version", itoa(current.Version))
		fail(c, http.StatusConflict, "VERSION_CONFLICT", "保险库已被其他设备更新，请先拉取最新版本")
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存保险库失败")
		return
	}
	// 多端实时同步：定向下发给本账号全部 Gateway 连接（含发起端，便于确认落库版本）。
	if h.deps.Bus != nil {
		h.deps.Bus.Publish(eventbus.Event{
			Type:    eventbus.EventVaultUpdate,
			UserIDs: []uuid.UUID{user.ID},
			Payload: gin.H{"version": saved.Version, "device_id": saved.DeviceID, "updated_at": saved.UpdatedAt},
		})
	}
	c.JSON(http.StatusOK, vaultView{
		Ciphertext: saved.Ciphertext, Nonce: saved.Nonce, KDFSalt: saved.KDFSalt,
		Algo: saved.Algo, Version: saved.Version, DeviceID: saved.DeviceID, UpdatedAt: &saved.UpdatedAt,
	})
}

// deleteVault DELETE /users/@me/vault：清空本账号保险库（多端登出/重置）。
func (h *api) deleteVault(c *gin.Context) {
	user := h.deps.CurrentUser(c)
	if err := h.deps.DB.Where("user_id = ?", user.ID).Delete(&model.SyncVault{}).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "清空保险库失败")
		return
	}
	if h.deps.Bus != nil {
		h.deps.Bus.Publish(eventbus.Event{
			Type: eventbus.EventVaultUpdate, UserIDs: []uuid.UUID{user.ID},
			Payload: gin.H{"version": 0, "deleted": true},
		})
	}
	c.Status(http.StatusNoContent)
}
