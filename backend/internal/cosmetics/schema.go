package cosmetics

import (
	"encoding/json"
	"fmt"
	"strings"
)

// CategorySchema 品类 schema（存在 CosmeticCategory.SchemaJSON）。
type CategorySchema struct {
	AssetSlots    []AssetSlotDef   `json:"asset_slots"`
	PayloadFields []PayloadFieldDef `json:"payload_fields,omitempty"`
	RenderHint    string           `json:"render_hint,omitempty"`
}

// AssetSlotDef 单个资产槽定义。
type AssetSlotDef struct {
	Key        string   `json:"key"`
	Label      string   `json:"label,omitempty"`
	Required   bool     `json:"required"`
	MIMEGroups []string `json:"mime_groups"` // image | animated_image | video | audio
	MaxBytes   int64    `json:"max_bytes,omitempty"`
}

// PayloadFieldDef 品类 payload 字段。
type PayloadFieldDef struct {
	Key     string   `json:"key"`
	Type    string   `json:"type"` // string | enum | bool | number | object | color
	Values  []string `json:"values,omitempty"`
	Default any      `json:"default,omitempty"`
}

func parseCategorySchema(raw string) (CategorySchema, error) {
	var s CategorySchema
	if strings.TrimSpace(raw) == "" || raw == "{}" {
		return s, nil
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return s, fmt.Errorf("schema_json 非法: %w", err)
	}
	return s, nil
}

func validateCategorySchema(s CategorySchema) error {
	if len(s.AssetSlots) == 0 && s.RenderHint == "" {
		// 允许仅配置型品类（如纯渐变铭牌可不强制资产）
	}
	seen := map[string]struct{}{}
	for _, slot := range s.AssetSlots {
		key := strings.TrimSpace(slot.Key)
		if key == "" {
			return fmt.Errorf("asset_slots.key 不能为空")
		}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("重复的 asset slot %q", key)
		}
		seen[key] = struct{}{}
		for _, g := range slot.MIMEGroups {
			switch g {
			case "image", "animated_image", "video", "audio":
			default:
				return fmt.Errorf("slot %q 未知 mime_group %q", key, g)
			}
		}
	}
	return nil
}

// mimeBelongsGroup 判断 MIME 是否属于声明的组。
func mimeBelongsGroup(mime string, groups []string) bool {
	mime = strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0]))
	for _, g := range groups {
		switch g {
		case "image":
			if mime == "image/png" || mime == "image/jpeg" || mime == "image/webp" {
				return true
			}
		case "animated_image":
			if mime == "image/gif" || mime == "image/png" || mime == "image/webp" || mime == "image/apng" {
				return true
			}
		case "video":
			if mime == "video/mp4" || mime == "video/webm" || mime == "application/mp4" {
				return true
			}
		case "audio":
			if mime == "audio/ogg" || mime == "audio/mpeg" || mime == "audio/mp3" ||
				mime == "audio/wav" || mime == "audio/wave" || mime == "audio/x-wav" {
				return true
			}
		}
	}
	return false
}

func maxBytesForSlot(slot AssetSlotDef) int64 {
	if slot.MaxBytes > 0 {
		return slot.MaxBytes
	}
	for _, g := range slot.MIMEGroups {
		if g == "audio" {
			return maxAudioBytes
		}
	}
	return defaultMaxAssetBytes
}

// assetsMap 解析 Item.AssetsJSON：slotKey -> asset id string or number.
func parseAssetsMap(raw string) (map[string]int64, error) {
	out := map[string]int64{}
	if strings.TrimSpace(raw) == "" || raw == "{}" {
		return out, nil
	}
	var generic map[string]any
	if err := json.Unmarshal([]byte(raw), &generic); err != nil {
		return nil, fmt.Errorf("assets_json 非法: %w", err)
	}
	for k, v := range generic {
		switch t := v.(type) {
		case string:
			id, err := parseSnowflakeString(t)
			if err != nil {
				return nil, fmt.Errorf("assets_json.%s 非法 id", k)
			}
			out[k] = id
		case float64:
			out[k] = int64(t)
		case json.Number:
			id, err := t.Int64()
			if err != nil {
				return nil, fmt.Errorf("assets_json.%s 非法 id", k)
			}
			out[k] = id
		default:
			return nil, fmt.Errorf("assets_json.%s 类型不支持", k)
		}
	}
	return out, nil
}

func encodeAssetsMap(m map[string]int64) string {
	if len(m) == 0 {
		return "{}"
	}
	// 输出 string id，前端统一按 string 处理
	strMap := make(map[string]string, len(m))
	for k, v := range m {
		strMap[k] = strID(v)
	}
	b, err := json.Marshal(strMap)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// validateItemAgainstSchema 发布前校验资产槽齐全。
func validateItemAgainstSchema(schema CategorySchema, assets map[string]int64, payload rawJSON) error {
	for _, slot := range schema.AssetSlots {
		if !slot.Required {
			continue
		}
		if assets[slot.Key] == 0 {
			return fmt.Errorf("缺少必填资产槽 %q", slot.Key)
		}
	}
	_ = payload
	return nil
}

type rawJSON = json.RawMessage
