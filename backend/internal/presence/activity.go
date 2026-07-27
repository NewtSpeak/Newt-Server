// Activity 结构化活动（Server-18）：正在玩 / 听 / 看 等。
// 按 Gateway session 上报，服务端跨端合并后随 PRESENCE_UPDATE / READY 下发。
package presence

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// Activity type 取值（docs 18 §3.2）。
const (
	ActivityPlaying   = "playing"
	ActivityListening = "listening"
	ActivityWatching  = "watching"
	ActivityStreaming = "streaming"
	ActivityCompeting = "competing"
	ActivityCustom    = "custom"
)

// Activity source 取值。
const (
	ActivitySourceDetected = "detected"
	ActivitySourceMedia    = "media"
	ActivitySourceSpotify  = "spotify"
	ActivitySourceRPC      = "rpc"
	ActivitySourceManual   = "manual"
)

// 约束（docs 18 M.4 / G.6）。
const (
	maxActivitiesPerSession = 3
	maxActivityNameLen      = 128
	maxActivityFieldLen     = 128
	// 封面 CDN URL 可能较长（含 query）；允许到 1024。
	maxActivityURLLen = 1024
)

// ActivityTimestamps 毫秒时间戳。
type ActivityTimestamps struct {
	Start *int64 `json:"start,omitempty"`
	End   *int64 `json:"end,omitempty"`
}

// ActivityAssets 可选资源图。
type ActivityAssets struct {
	LargeImage string `json:"large_image,omitempty"`
	LargeText  string `json:"large_text,omitempty"`
	SmallImage string `json:"small_image,omitempty"`
	SmallText  string `json:"small_text,omitempty"`
}

// Activity 一条结构化活动。
type Activity struct {
	Type          string              `json:"type"`
	Name          string              `json:"name"`
	Details       string              `json:"details,omitempty"`
	State         string              `json:"state,omitempty"`
	ApplicationID string              `json:"application_id,omitempty"`
	URL           string              `json:"url,omitempty"`
	Assets        *ActivityAssets     `json:"assets,omitempty"`
	Timestamps    *ActivityTimestamps `json:"timestamps,omitempty"`
	Source        string              `json:"source"`
	// SessionID 仅服务端内部合并用，不下发他人。
	SessionID string `json:"-"`
}

// typeRank 主活动优先级：playing > streaming > listening > watching ≈ competing > custom。
func typeRank(t string) int {
	switch t {
	case ActivityPlaying:
		return 5
	case ActivityStreaming:
		return 4
	case ActivityListening:
		return 3
	case ActivityWatching, ActivityCompeting:
		return 2
	case ActivityCustom:
		return 1
	default:
		return 0
	}
}

func validActivityType(t string) bool { return typeRank(t) > 0 }

func validActivitySource(s string) bool {
	switch s {
	case ActivitySourceDetected, ActivitySourceMedia, ActivitySourceSpotify,
		ActivitySourceRPC, ActivitySourceManual:
		return true
	}
	return false
}

func truncateRunes(s string, max int) string {
	if max <= 0 || s == "" {
		return s
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max])
}

// sanitizeAssetURL 允许 http(s) 或本站公开资源相对路径 /public-assets/…；拒绝 data: 等。
func sanitizeAssetURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") {
		return truncateRunes(s, maxActivityURLLen)
	}
	// 本站活动封面 / 头像等公开资源
	if strings.HasPrefix(s, "/public-assets/") {
		return truncateRunes(s, maxActivityURLLen)
	}
	return ""
}

// SanitizeActivities 校验并规范化上行活动列表：丢弃非法条、截断字段、最多 3 条。
// 返回的切片可安全写入 session（nil 与空切片语义由调用方区分）。
func SanitizeActivities(raw []Activity) []Activity {
	if len(raw) == 0 {
		return []Activity{}
	}
	out := make([]Activity, 0, maxActivitiesPerSession)
	for _, a := range raw {
		if len(out) >= maxActivitiesPerSession {
			break
		}
		a.Type = strings.TrimSpace(a.Type)
		a.Name = strings.TrimSpace(a.Name)
		a.Source = strings.TrimSpace(a.Source)
		if !validActivityType(a.Type) || a.Name == "" || !validActivitySource(a.Source) {
			continue
		}
		a.Name = truncateRunes(a.Name, maxActivityNameLen)
		a.Details = truncateRunes(strings.TrimSpace(a.Details), maxActivityFieldLen)
		a.State = truncateRunes(strings.TrimSpace(a.State), maxActivityFieldLen)
		a.ApplicationID = truncateRunes(strings.TrimSpace(a.ApplicationID), 64)
		a.URL = sanitizeAssetURL(a.URL)
		if a.Assets != nil {
			a.Assets.LargeImage = sanitizeAssetURL(a.Assets.LargeImage)
			a.Assets.LargeText = truncateRunes(strings.TrimSpace(a.Assets.LargeText), maxActivityFieldLen)
			a.Assets.SmallImage = sanitizeAssetURL(a.Assets.SmallImage)
			a.Assets.SmallText = truncateRunes(strings.TrimSpace(a.Assets.SmallText), maxActivityFieldLen)
			if a.Assets.LargeImage == "" && a.Assets.LargeText == "" &&
				a.Assets.SmallImage == "" && a.Assets.SmallText == "" {
				a.Assets = nil
			}
		}
		a.SessionID = "" // 上行不可伪造
		out = append(out, a)
	}
	return out
}

func activityDedupeKey(a Activity) string {
	return a.Type + "\x00" + strings.ToLower(strings.TrimSpace(a.Name)) + "\x00" + a.Source
}

func activityStartMS(a Activity) int64 {
	if a.Timestamps != nil && a.Timestamps.Start != nil {
		return *a.Timestamps.Start
	}
	return 0
}

func hasCover(a Activity) bool {
	return a.Assets != nil && strings.TrimSpace(a.Assets.LargeImage) != ""
}

// mergeActivities 跨 session 合并：去重后按 type 优先级排序，最多 3 条（docs 18 §4.3）。
// sessionPresence 定义见 presence.go（同 package）。
func mergeActivities(sessions map[string]sessionPresence) []Activity {
	if len(sessions) == 0 {
		return nil
	}
	best := make(map[string]Activity, 4)
	for sid, sp := range sessions {
		for _, a := range sp.activities {
			key := activityDedupeKey(a)
			a.SessionID = sid
			prev, ok := best[key]
			if !ok {
				best[key] = a
				continue
			}
			// 同键优先保留有封面的；否则保留 start 更早者
			if !hasCover(prev) && hasCover(a) {
				best[key] = a
				continue
			}
			if hasCover(prev) && !hasCover(a) {
				continue
			}
			ps, cs := activityStartMS(prev), activityStartMS(a)
			if cs > 0 && (ps == 0 || cs < ps) {
				best[key] = a
			}
		}
	}
	if len(best) == 0 {
		return nil
	}
	list := make([]Activity, 0, len(best))
	for _, a := range best {
		list = append(list, a)
	}
	sort.SliceStable(list, func(i, j int) bool {
		ri, rj := typeRank(list[i].Type), typeRank(list[j].Type)
		if ri != rj {
			return ri > rj
		}
		si, sj := activityStartMS(list[i]), activityStartMS(list[j])
		if si != sj {
			// 有 start 的优先；start 更早优先
			if si == 0 {
				return false
			}
			if sj == 0 {
				return true
			}
			return si < sj
		}
		return list[i].Name < list[j].Name
	})
	if len(list) > maxActivitiesPerSession {
		list = list[:maxActivitiesPerSession]
	}
	// 对外下发去掉 session_id
	for i := range list {
		list[i].SessionID = ""
	}
	return list
}

// equalActivities 深度相等（忽略 SessionID）。
func equalActivities(a, b []Activity) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !equalActivity(a[i], b[i]) {
			return false
		}
	}
	return true
}

func equalActivity(a, b Activity) bool {
	if a.Type != b.Type || a.Name != b.Name || a.Details != b.Details ||
		a.State != b.State || a.ApplicationID != b.ApplicationID ||
		a.URL != b.URL || a.Source != b.Source {
		return false
	}
	if !equalTimestamps(a.Timestamps, b.Timestamps) {
		return false
	}
	return equalAssets(a.Assets, b.Assets)
}

func equalTimestamps(a, b *ActivityTimestamps) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return equalInt64Ptr(a.Start, b.Start) && equalInt64Ptr(a.End, b.End)
}

func equalInt64Ptr(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func equalAssets(a, b *ActivityAssets) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.LargeImage == b.LargeImage && a.LargeText == b.LargeText &&
		a.SmallImage == b.SmallImage && a.SmallText == b.SmallText
}

// cloneActivities 浅拷贝切片（元素为值类型）。
func cloneActivities(in []Activity) []Activity {
	if len(in) == 0 {
		return nil
	}
	out := make([]Activity, len(in))
	copy(out, in)
	return out
}
