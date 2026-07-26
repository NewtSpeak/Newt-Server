package cosmetics

import (
	"encoding/json"
	"time"

	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SeedCategories 幂等写入首期 4 个品类（不覆盖运营已改的 name/description/enabled）。
func SeedCategories(db *gorm.DB) error {
	now := time.Now().UTC()
	seeds := []model.CosmeticCategory{
		{
			Key: "avatar_frame", Name: "头像框", Description: "环绕头像的静态或动态边框",
			Slot: "avatar_frame", SortOrder: 10, Enabled: true,
			SchemaJSON: mustJSON(CategorySchema{
				RenderHint: "avatar_frame",
				AssetSlots: []AssetSlotDef{
					{Key: "primary", Label: "边框图", Required: true, MIMEGroups: []string{"image", "animated_image", "video"}, MaxBytes: 8 << 20},
				},
				PayloadFields: []PayloadFieldDef{
					{Key: "motion", Type: "enum", Values: []string{"static", "animated"}, Default: "static"},
				},
			}),
			CreatedAt: now, UpdatedAt: now,
		},
		{
			Key: "profile_border", Name: "资料卡片边框", Description: "适配紧凑/完整两种尺寸的资料卡边框",
			Slot: "profile_border", SortOrder: 20, Enabled: true,
			SchemaJSON: mustJSON(CategorySchema{
				RenderHint: "profile_border",
				AssetSlots: []AssetSlotDef{
					{Key: "compact", Label: "紧凑卡片边框", Required: true, MIMEGroups: []string{"image", "animated_image", "video"}, MaxBytes: 8 << 20},
					{Key: "full", Label: "完整卡片边框", Required: true, MIMEGroups: []string{"image", "animated_image", "video"}, MaxBytes: 12 << 20},
				},
				PayloadFields: []PayloadFieldDef{
					{Key: "motion", Type: "enum", Values: []string{"static", "animated"}, Default: "static"},
				},
			}),
			CreatedAt: now, UpdatedAt: now,
		},
		{
			Key: "profile_effect", Name: "资料卡内特效", Description: "资料卡内容区特效，可附带音效",
			Slot: "profile_effect", SortOrder: 30, Enabled: true,
			SchemaJSON: mustJSON(CategorySchema{
				RenderHint: "profile_effect",
				AssetSlots: []AssetSlotDef{
					{Key: "visual", Label: "视觉特效", Required: true, MIMEGroups: []string{"image", "animated_image", "video"}, MaxBytes: 12 << 20},
					{Key: "audio", Label: "音效（可选）", Required: false, MIMEGroups: []string{"audio"}, MaxBytes: 2 << 20},
				},
				PayloadFields: []PayloadFieldDef{
					{Key: "motion", Type: "enum", Values: []string{"static", "animated"}, Default: "animated"},
					{Key: "audio_loop", Type: "bool", Default: false},
				},
			}),
			CreatedAt: now, UpdatedAt: now,
		},
		{
			Key: "nameplate", Name: "铭牌", Description: "成员列表用户行背景：渐变或视频（视频需底色）",
			Slot: "nameplate", SortOrder: 40, Enabled: true,
			SchemaJSON: mustJSON(CategorySchema{
				RenderHint: "nameplate",
				AssetSlots: []AssetSlotDef{
					{Key: "video", Label: "视频背景（可选）", Required: false, MIMEGroups: []string{"video"}, MaxBytes: 12 << 20},
					{Key: "static", Label: "静态背景图（可选）", Required: false, MIMEGroups: []string{"image", "animated_image"}, MaxBytes: 8 << 20},
				},
				PayloadFields: []PayloadFieldDef{
					{Key: "mode", Type: "enum", Values: []string{"gradient", "video", "image"}, Default: "gradient"},
					{Key: "base_color", Type: "color", Default: "#1e1f22"},
					{Key: "gradient", Type: "object"},
					{Key: "video_opacity", Type: "number", Default: 0.85},
				},
			}),
			CreatedAt: now, UpdatedAt: now,
		},
	}

	for _, cat := range seeds {
		// 仅插入缺失 key；已存在不覆盖运营修改
		err := db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoNothing: true,
		}).Create(&cat).Error
		if err != nil {
			return err
		}
	}
	return nil
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
