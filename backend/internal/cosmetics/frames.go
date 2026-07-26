package cosmetics

// 管理端批量头像框查询：管理后台各列表页给任意用户头像叠加头像框用。
//   GET /admin/cosmetics/avatar-frames?ids=<uuid>,<uuid>,...（≤200）
// 返回 {frames: {"<uuid>": EquippedSlotView}}，仅含装备了头像框的用户。

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const maxFrameQueryIDs = 200

// adminAvatarFrames GET /admin/cosmetics/avatar-frames?ids=...
func (h *api) adminAvatarFrames(c *gin.Context) {
	raw := strings.TrimSpace(c.Query("ids"))
	if raw == "" {
		c.JSON(http.StatusOK, gin.H{"frames": map[string]EquippedSlotView{}})
		return
	}
	parts := strings.Split(raw, ",")
	if len(parts) > maxFrameQueryIDs {
		fail(c, http.StatusBadRequest, "TOO_MANY_IDS", "一次最多查询 200 个用户")
		return
	}
	ids := make([]uuid.UUID, 0, len(parts))
	seen := map[uuid.UUID]struct{}{}
	for _, p := range parts {
		id, err := uuid.Parse(strings.TrimSpace(p))
		if err != nil {
			continue // 非法 id 静默跳过（批量查询容错）
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	resolved := ResolveEquippedForUsers(h.db(), ids, true)
	frames := make(map[string]EquippedSlotView, len(resolved))
	for userID, slots := range resolved {
		if frame, ok := slots["avatar_frame"]; ok {
			frames[userID.String()] = frame
		}
	}
	c.JSON(http.StatusOK, gin.H{"frames": frames})
}
