package message

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
	"github.com/newtspeak/newt-server/backend/internal/audit"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/perms"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 语音包完整模型（docs 12 FR-09~FR-16）：包 CRUD（服管）、音频上传（公开资产模式，
// 参考 userapi 头像）、用户按服选包与 RARE 身份组授权。服级默认 audio_url 配置
//（voicepack.go GuildVoicePackConfig）保留为「无选包时的回退默认包」。

// maxVoicePackAudioBytes 音频大小上限 500KB（docs 12 FR-02 量级；时长限制由客户端保证）。
const maxVoicePackAudioBytes = int64(500 << 10)

func voicePackDir(dataDir string) string { return filepath.Join(dataDir, "voicepacks") }

// sniffVoicePackAudio 以文件魔数判定音频格式（不信任声明的 Content-Type）：
// 仅支持 OGG（OggS 容器）与 MP3（ID3 标签或 MPEG 帧同步字）。
func sniffVoicePackAudio(head []byte) (ext, contentType string, ok bool) {
	if len(head) >= 4 && string(head[:4]) == "OggS" {
		return "ogg", "audio/ogg", true
	}
	if len(head) >= 3 && string(head[:3]) == "ID3" {
		return "mp3", "audio/mpeg", true
	}
	// 无 ID3 标签的裸 MP3：MPEG 音频帧同步字 11 位全 1（0xFF + 高 3 位 1）。
	if len(head) >= 2 && head[0] == 0xFF && head[1]&0xE0 == 0xE0 {
		return "mp3", "audio/mpeg", true
	}
	return "", "", false
}

// packAuthorized 判定用户当前是否有权使用某语音包（docs 12 5A.4 / FR-12）：
//   - STANDARD：任何成员可用；
//   - RARE：须持有 allowed_role_ids 中任一身份组（含 @everyone 角色被显式授权的情形）。
//
// 触发播放与用户选包共用本函数，保证「失去身份组后已选中的 RARE 包自动失效」。
func packAuthorized(db *gorm.DB, pack model.VoicePack, userID uuid.UUID) bool {
	var member model.Member
	if err := db.First(&member, "guild_id = ? AND user_id = ?", pack.GuildID, userID).Error; err != nil {
		return false
	}
	if pack.Kind != model.VoicePackRare {
		return true
	}
	if len(pack.AllowedRoleIDs) == 0 {
		return false
	}
	roleIDs := []uuid.UUID(pack.AllowedRoleIDs)
	var count int64
	db.Model(&model.MemberRole{}).
		Where("member_id = ? AND role_id IN ?", member.ID, roleIDs).Count(&count)
	if count > 0 {
		return true
	}
	// @everyone 角色不落 member_roles 行：被显式授权时全员视为持有。
	db.Model(&model.Role{}).
		Where("guild_id = ? AND is_everyone = true AND id IN ?", pack.GuildID, roleIDs).Count(&count)
	return count > 0
}

// guildManageAccess 加载权限上下文并要求 MANAGE_GUILD（服管，docs 12 5A.4）。
func (s *service) guildManageAccess(c *gin.Context) (*perms.GuildContext, bool) {
	ctx, ok := s.guildAccess(c)
	if !ok {
		return nil, false
	}
	if !ctx.Has(rbac.ManageGuild) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "缺少管理服务器权限")
		return nil, false
	}
	return ctx, true
}

// loadGuildPack 取指定服下的语音包；不存在或不属于该服一律 404。
func (s *service) loadGuildPack(c *gin.Context, guildID uuid.UUID) (model.VoicePack, bool) {
	packID, ok := parseUUIDParam(c, "packID")
	if !ok {
		return model.VoicePack{}, false
	}
	var pack model.VoicePack
	if err := s.db.First(&pack, "id = ? AND guild_id = ?", packID, guildID).Error; err != nil {
		notFound(c)
		return model.VoicePack{}, false
	}
	return pack, true
}

// ---------------------------------------------------------------------------
// 包 CRUD（服管）
// ---------------------------------------------------------------------------

type voicePackCreateRequest struct {
	Name           string      `json:"name" binding:"required,min=1,max=100"`
	Kind           string      `json:"kind" binding:"omitempty"`
	AllowedRoleIDs []uuid.UUID `json:"allowed_role_ids"`
	Enabled        *bool       `json:"enabled"`
	DurationMS     int         `json:"duration_ms" binding:"omitempty,min=0"`
}

func parseVoicePackKind(raw string) (model.VoicePackKind, bool) {
	switch model.VoicePackKind(raw) {
	case "", model.VoicePackStandard:
		return model.VoicePackStandard, true
	case model.VoicePackRare:
		return model.VoicePackRare, true
	}
	return "", false
}

// validateAllowedRoles 校验授权身份组均属于该服（防跨服角色 ID 注入）。
func (s *service) validateAllowedRoles(guildID uuid.UUID, roleIDs []uuid.UUID) bool {
	if len(roleIDs) == 0 {
		return true
	}
	var count int64
	s.db.Model(&model.Role{}).Where("guild_id = ? AND id IN ?", guildID, roleIDs).Count(&count)
	return count == int64(len(roleIDs))
}

// createVoicePack POST /guilds/{gid}/voice-packs：服管新建语音包（docs 12 FR-13）。
// 音频经独立上传端点写入；未上传音频的包不会被触发播放。
func (s *service) createVoicePack(c *gin.Context) {
	ctx, ok := s.guildManageAccess(c)
	if !ok {
		return
	}
	var input voicePackCreateRequest
	if !bind(c, &input) {
		return
	}
	kind, ok := parseVoicePackKind(input.Kind)
	if !ok {
		fail(c, http.StatusBadRequest, "INVALID_KIND", "kind 需为 STANDARD 或 RARE")
		return
	}
	if !s.validateAllowedRoles(ctx.Guild.ID, input.AllowedRoleIDs) {
		fail(c, http.StatusBadRequest, "INVALID_ROLES", "allowed_role_ids 含不属于该服务器的角色")
		return
	}
	actor := s.currentUser(c)
	pack := model.VoicePack{
		ID: uuid.New(), GuildID: ctx.Guild.ID, Name: input.Name,
		Kind: kind, AllowedRoleIDs: model.UUIDList(input.AllowedRoleIDs),
		Enabled: input.Enabled == nil || *input.Enabled,
		DurationMS: input.DurationMS, CreatedBy: actor.ID,
	}
	if err := s.db.Create(&pack).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "创建语音包失败")
		return
	}
	// enabled 列带 default:true：GORM Create 跳过零值 false，创建即停用需显式补写。
	if !pack.Enabled {
		if err := s.db.Model(&model.VoicePack{}).Where("id = ?", pack.ID).
			Update("enabled", false).Error; err != nil {
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "创建语音包失败")
			return
		}
	}
	audit.Log(s.db, audit.Entry{
		ActorID: &actor.ID, ActorType: "guild_admin", GuildID: &ctx.Guild.ID,
		Action: "voicepack.create", TargetType: "voice_pack", TargetID: pack.ID.String(),
		Detail: map[string]any{"name": pack.Name, "kind": pack.Kind},
	})
	s.publishVoicePackUpdate(ctx.Guild.ID, map[string]any{"pack_id": pack.ID, "pack": pack})
	c.JSON(http.StatusCreated, pack)
}

// voicePackView 列表视图：附当前用户的可用性与选中标记（docs 12 FR-09/FR-10
// 「不可用的稀有包置灰展示 + 获取条件可见」）。
type voicePackView struct {
	model.VoicePack
	Available bool `json:"available"`
	Selected  bool `json:"selected"`
}

// listVoicePacks GET /guilds/{gid}/voice-packs：成员可读（选包列表）；
// 普通成员仅见启用中的包，服管（MANAGE_GUILD）另见停用包（管理页）。
func (s *service) listVoicePacks(c *gin.Context) {
	ctx, ok := s.guildAccess(c)
	if !ok {
		return
	}
	user := s.currentUser(c)
	query := s.db.Where("guild_id = ?", ctx.Guild.ID).Order("created_at ASC")
	if !ctx.Has(rbac.ManageGuild) {
		query = query.Where("enabled = true")
	}
	var packs []model.VoicePack
	if err := query.Find(&packs).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "查询语音包失败")
		return
	}
	var selection model.VoicePackSelection
	hasSelection := s.db.First(&selection, "guild_id = ? AND user_id = ?", ctx.Guild.ID, user.ID).Error == nil
	views := make([]voicePackView, 0, len(packs))
	for _, pack := range packs {
		views = append(views, voicePackView{
			VoicePack: pack,
			Available: pack.Enabled && packAuthorized(s.db, pack, user.ID),
			Selected:  hasSelection && selection.PackID == pack.ID,
		})
	}
	c.JSON(http.StatusOK, gin.H{"voice_packs": views})
}

type voicePackPatchPackRequest struct {
	Name           *string      `json:"name" binding:"omitempty,min=1,max=100"`
	Kind           *string      `json:"kind"`
	AllowedRoleIDs *[]uuid.UUID `json:"allowed_role_ids"`
	Enabled        *bool        `json:"enabled"`
	DurationMS     *int         `json:"duration_ms" binding:"omitempty,min=0"`
}

// patchVoicePack PATCH /guilds/{gid}/voice-packs/{packID}：服管更新包属性。
func (s *service) patchVoicePack(c *gin.Context) {
	ctx, ok := s.guildManageAccess(c)
	if !ok {
		return
	}
	pack, ok := s.loadGuildPack(c, ctx.Guild.ID)
	if !ok {
		return
	}
	var input voicePackPatchPackRequest
	if !bind(c, &input) {
		return
	}
	if input.Name != nil {
		pack.Name = *input.Name
	}
	if input.Kind != nil {
		kind, valid := parseVoicePackKind(*input.Kind)
		if !valid {
			fail(c, http.StatusBadRequest, "INVALID_KIND", "kind 需为 STANDARD 或 RARE")
			return
		}
		pack.Kind = kind
	}
	if input.AllowedRoleIDs != nil {
		if !s.validateAllowedRoles(ctx.Guild.ID, *input.AllowedRoleIDs) {
			fail(c, http.StatusBadRequest, "INVALID_ROLES", "allowed_role_ids 含不属于该服务器的角色")
			return
		}
		pack.AllowedRoleIDs = model.UUIDList(*input.AllowedRoleIDs)
	}
	if input.Enabled != nil {
		pack.Enabled = *input.Enabled
	}
	if input.DurationMS != nil {
		pack.DurationMS = *input.DurationMS
	}
	if err := s.db.Save(&pack).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "更新语音包失败")
		return
	}
	actor := s.currentUser(c)
	audit.Log(s.db, audit.Entry{
		ActorID: &actor.ID, ActorType: "guild_admin", GuildID: &ctx.Guild.ID,
		Action: "voicepack.update", TargetType: "voice_pack", TargetID: pack.ID.String(),
		Detail: map[string]any{"name": pack.Name, "kind": pack.Kind, "enabled": pack.Enabled},
	})
	s.publishVoicePackUpdate(ctx.Guild.ID, map[string]any{"pack_id": pack.ID, "pack": pack})
	c.JSON(http.StatusOK, pack)
}

// deleteVoicePack DELETE /guilds/{gid}/voice-packs/{packID}：删除包、清理选中记录与音频文件。
func (s *service) deleteVoicePack(c *gin.Context) {
	ctx, ok := s.guildManageAccess(c)
	if !ok {
		return
	}
	pack, ok := s.loadGuildPack(c, ctx.Guild.ID)
	if !ok {
		return
	}
	// 删除前收集选中该包的用户：连带清除其选择后需定向通知（多端选包状态同步）。
	var affectedUserIDs []uuid.UUID
	s.db.Model(&model.VoicePackSelection{}).Where("pack_id = ?", pack.ID).Pluck("user_id", &affectedUserIDs)
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("pack_id = ?", pack.ID).Delete(&model.VoicePackSelection{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.VoicePack{}, "id = ?", pack.ID).Error
	}); err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "删除语音包失败")
		return
	}
	removeVoicePackFile(voicePackDir(s.cfg.DataDir), pack.AudioURL)
	actor := s.currentUser(c)
	audit.Log(s.db, audit.Entry{
		ActorID: &actor.ID, ActorType: "guild_admin", GuildID: &ctx.Guild.ID,
		Action: "voicepack.delete", TargetType: "voice_pack", TargetID: pack.ID.String(),
		Detail: map[string]any{"name": pack.Name},
	})
	s.publishVoicePackUpdate(ctx.Guild.ID, map[string]any{"pack_id": pack.ID, "deleted": true})
	if len(affectedUserIDs) > 0 {
		// 定向受影响的选包用户：其选择已被连带清除（selection=null）。
		s.publishVoicePackUpdate(ctx.Guild.ID, map[string]any{
			"pack_id": pack.ID, "deleted": true, "selection": nil,
		}, affectedUserIDs...)
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// ---------------------------------------------------------------------------
// 音频上传（服管；公开资产模式，参考 userapi 头像）
// ---------------------------------------------------------------------------

// uploadVoicePackAudio POST /guilds/{gid}/voice-packs/{packID}/audio：
// multipart 上传（字段名 file），≤500KB，仅 OGG/MP3（以魔数嗅探为准）；
// 可选表单字段 duration_ms（客户端自报时长，不强校验）。文件名带纳秒版本号，
// URL 不可变可长缓存；换包即换 URL，客户端 LRU 缓存自然失效（docs 12 §5.2）。
func (s *service) uploadVoicePackAudio(c *gin.Context) {
	ctx, ok := s.guildManageAccess(c)
	if !ok {
		return
	}
	pack, ok := s.loadGuildPack(c, ctx.Guild.ID)
	if !ok {
		return
	}
	fileHeader, err := c.FormFile("file")
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "缺少 multipart 文件字段 file")
		return
	}
	if fileHeader.Size > maxVoicePackAudioBytes {
		fail(c, http.StatusBadRequest, "FILE_TOO_LARGE", fmt.Sprintf("语音包音频不能超过 %d 字节", maxVoicePackAudioBytes))
		return
	}
	file, err := fileHeader.Open()
	if err != nil {
		fail(c, http.StatusBadRequest, "UPLOAD_FAILED", "读取上传内容失败")
		return
	}
	defer file.Close()

	head := make([]byte, 512)
	n, _ := io.ReadFull(file, head)
	ext, _, ok := sniffVoicePackAudio(head[:n])
	if !ok {
		fail(c, http.StatusBadRequest, "UNSUPPORTED_TYPE", "语音包音频仅支持 OGG/MP3")
		return
	}

	dir := voicePackDir(s.cfg.DataDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(c, http.StatusInternalServerError, "STORAGE_ERROR", "创建存储目录失败")
		return
	}
	filename := fmt.Sprintf("%s-pack-%d.%s", pack.ID, time.Now().UTC().UnixNano(), ext)
	target := filepath.Join(dir, filename)
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		fail(c, http.StatusInternalServerError, "STORAGE_ERROR", "写入文件失败")
		return
	}
	written, err := out.Write(head[:n])
	if err == nil {
		var copied int64
		copied, err = io.Copy(out, io.LimitReader(file, maxVoicePackAudioBytes+1))
		written += int(copied)
	}
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil || written == 0 || int64(written) > maxVoicePackAudioBytes {
		_ = os.Remove(target)
		if int64(written) > maxVoicePackAudioBytes {
			fail(c, http.StatusBadRequest, "FILE_TOO_LARGE", fmt.Sprintf("语音包音频不能超过 %d 字节", maxVoicePackAudioBytes))
			return
		}
		fail(c, http.StatusBadRequest, "UPLOAD_FAILED", "上传内容为空或写入失败")
		return
	}

	url := "/public-assets/voicepacks/" + filename
	updates := map[string]any{"audio_url": url, "size_bytes": int64(written)}
	if raw := c.PostForm("duration_ms"); raw != "" {
		if durationMS, parseErr := strconv.Atoi(raw); parseErr == nil && durationMS >= 0 {
			updates["duration_ms"] = durationMS
		}
	}
	if err := s.db.Model(&model.VoicePack{}).Where("id = ?", pack.ID).Updates(updates).Error; err != nil {
		_ = os.Remove(target)
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存语音包失败")
		return
	}
	removeVoicePackFile(dir, pack.AudioURL)

	var fresh model.VoicePack
	if err := s.db.First(&fresh, "id = ?", pack.ID).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取语音包失败")
		return
	}
	actor := s.currentUser(c)
	audit.Log(s.db, audit.Entry{
		ActorID: &actor.ID, ActorType: "guild_admin", GuildID: &ctx.Guild.ID,
		Action: "voicepack.upload_audio", TargetType: "voice_pack", TargetID: pack.ID.String(),
		Detail: map[string]any{"audio_url": url, "size_bytes": written},
	})
	s.publishVoicePackUpdate(ctx.Guild.ID, map[string]any{"pack_id": fresh.ID, "pack": fresh})
	c.JSON(http.StatusOK, fresh)
}

// removeVoicePackFile 按历史 URL 删除磁盘文件（仅接受本模块生成的公开资产路径）。
func removeVoicePackFile(dir, url string) {
	if url == "" || !strings.HasPrefix(url, "/public-assets/voicepacks/") {
		return
	}
	name := path.Base(url)
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return
	}
	if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = err
	}
}

// RegisterPublicAssets 挂载语音包音频公开访问路由（/public-assets/voicepacks/{name}，
// 无需登录；文件名含纳秒版本号不可变，允许 CDN/浏览器长缓存，docs 12 §5.1 CDN 直拉）。
func RegisterPublicAssets(pub *gin.RouterGroup, deps appdeps.Deps) error {
	dataDir := deps.Cfg.DataDir
	pub.GET("/voicepacks/:name", func(c *gin.Context) { serveVoicePackAsset(c, dataDir) })
	return nil
}

func serveVoicePackAsset(c *gin.Context, dataDir string) {
	name := c.Param("name")
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		c.Status(http.StatusNotFound)
		return
	}
	contentTypes := map[string]string{".ogg": "audio/ogg", ".mp3": "audio/mpeg"}
	contentType, ok := contentTypes[strings.ToLower(filepath.Ext(name))]
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	target := filepath.Join(voicePackDir(dataDir), name)
	if _, err := os.Stat(target); err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Content-Type", contentType)
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.File(target)
}

// ---------------------------------------------------------------------------
// 用户选包（docs 12 FR-09/FR-12）
// ---------------------------------------------------------------------------

// selectVoicePack PUT /guilds/{gid}/voice-packs/{packID}/select：成员选用语音包。
// STANDARD 任何成员可选；RARE 须持有 allowed_role_ids 之一（越权保存被拒，FR-12）。
func (s *service) selectVoicePack(c *gin.Context) {
	ctx, ok := s.guildAccess(c)
	if !ok {
		return
	}
	if ctx.Member == nil {
		notFound(c)
		return
	}
	pack, ok := s.loadGuildPack(c, ctx.Guild.ID)
	if !ok {
		return
	}
	user := s.currentUser(c)
	if !pack.Enabled {
		fail(c, http.StatusForbidden, "PACK_DISABLED", "该语音包已停用")
		return
	}
	if !packAuthorized(s.db, pack, user.ID) {
		fail(c, http.StatusForbidden, "PACK_NOT_AUTHORIZED", "缺少使用该语音包所需的身份组")
		return
	}
	selection := model.VoicePackSelection{UserID: user.ID, GuildID: ctx.Guild.ID, PackID: pack.ID}
	if err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "guild_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"pack_id", "updated_at"}),
	}).Create(&selection).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存选包失败")
		return
	}
	// 定向本人全部端：多端选包状态实时同步。
	s.publishVoicePackUpdate(ctx.Guild.ID, map[string]any{"selection": gin.H{"pack_id": pack.ID}}, user.ID)
	c.JSON(http.StatusOK, gin.H{"selected": true, "pack_id": pack.ID})
}

// getMyVoicePackSelection GET /guilds/{gid}/voice-packs/@me：查询当前选择。
// available 标记授权是否仍有效（失去身份组后客户端据此提示回退，FR-12）。
func (s *service) getMyVoicePackSelection(c *gin.Context) {
	ctx, ok := s.guildAccess(c)
	if !ok {
		return
	}
	user := s.currentUser(c)
	var selection model.VoicePackSelection
	if err := s.db.First(&selection, "guild_id = ? AND user_id = ?", ctx.Guild.ID, user.ID).Error; err != nil {
		c.JSON(http.StatusOK, gin.H{"selection": nil})
		return
	}
	var pack model.VoicePack
	if err := s.db.First(&pack, "id = ?", selection.PackID).Error; err != nil {
		c.JSON(http.StatusOK, gin.H{"selection": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"selection": voicePackView{
		VoicePack: pack,
		Available: pack.Enabled && packAuthorized(s.db, pack, user.ID),
		Selected:  true,
	}})
}

// clearMyVoicePackSelection DELETE /guilds/{gid}/voice-packs/@me：取消选择（回退「不使用」）。
func (s *service) clearMyVoicePackSelection(c *gin.Context) {
	ctx, ok := s.guildAccess(c)
	if !ok {
		return
	}
	user := s.currentUser(c)
	if err := s.db.Where("guild_id = ? AND user_id = ?", ctx.Guild.ID, user.ID).
		Delete(&model.VoicePackSelection{}).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "取消选包失败")
		return
	}
	// 定向本人全部端：多端选包状态实时同步（selection=null 表示已取消）。
	s.publishVoicePackUpdate(ctx.Guild.ID, map[string]any{"selection": nil}, user.ID)
	c.JSON(http.StatusOK, gin.H{"selected": false})
}
