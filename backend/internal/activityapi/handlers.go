// Package activityapi 活动封面与游戏目录（Server-18）：
//   GET /activity/game-catalog     内置游戏目录（含封面 URL）
//   GET /activity/resolve-cover    按名称解析游戏/音乐封面（音乐走 iTunes Search）
package activityapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
)

type api struct {
	deps   appdeps.Deps
	client *http.Client
}

// Register 挂载到平面根组（/api/v1 或 /gapi/v1）；需登录。
func Register(group *gin.RouterGroup, deps appdeps.Deps) error {
	h := &api{
		deps: deps,
		client: &http.Client{
			Timeout: 6 * time.Second,
		},
	}
	authed := group.Group("", deps.Auth)
	authed.GET("/activity/game-catalog", h.gameCatalog)
	authed.GET("/activity/resolve-cover", h.resolveCover)
	authed.POST("/activity/cover", h.uploadCover)
	return nil
}

type catalogResponse struct {
	Version int         `json:"version"`
	Games   []gameEntry `json:"games"`
}

func (h *api) gameCatalog(c *gin.Context) {
	c.JSON(http.StatusOK, catalogResponse{Version: 1, Games: builtInCatalog()})
}

type coverResponse struct {
	Kind     string `json:"kind"` // game | music
	Name     string `json:"name,omitempty"`
	Details  string `json:"details,omitempty"` // 音乐：艺人
	CoverURL string `json:"cover_url,omitempty"`
	Source   string `json:"source,omitempty"` // catalog | itunes | none
}

func (h *api) resolveCover(c *gin.Context) {
	kind := strings.ToLower(strings.TrimSpace(c.Query("kind")))
	name := strings.TrimSpace(c.Query("name"))
	artist := strings.TrimSpace(c.Query("artist"))
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_REQUEST", "message": "name 必填"}})
		return
	}
	if utf8Len(name) > 128 || utf8Len(artist) > 128 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_REQUEST", "message": "参数过长"}})
		return
	}
	switch kind {
	case "game", "playing":
		if entry, ok := matchGame(name); ok {
			c.JSON(http.StatusOK, coverResponse{
				Kind: "game", Name: entry.Name, CoverURL: entry.CoverURL, Source: "catalog",
			})
			return
		}
		c.JSON(http.StatusOK, coverResponse{Kind: "game", Name: name, Source: "none"})
	case "music", "listening":
		cover, track, art, err := h.lookupITunes(name, artist)
		if err != nil || cover == "" {
			c.JSON(http.StatusOK, coverResponse{Kind: "music", Name: name, Details: artist, Source: "none"})
			return
		}
		if track == "" {
			track = name
		}
		c.JSON(http.StatusOK, coverResponse{
			Kind: "music", Name: track, Details: art, CoverURL: cover, Source: "itunes",
		})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_REQUEST", "message": "kind 须为 game 或 music"}})
	}
}

func matchGame(query string) (gameEntry, bool) {
	q := normalizeKey(query)
	if q == "" {
		return gameEntry{}, false
	}
	for _, g := range builtInCatalog() {
		if normalizeKey(g.Name) == q {
			return g, true
		}
		for _, a := range g.Aliases {
			if normalizeKey(a) == q {
				return g, true
			}
		}
		// 包含匹配（「原神启动」→ 原神）
		if strings.Contains(q, normalizeKey(g.Name)) || strings.Contains(normalizeKey(g.Name), q) {
			return g, true
		}
		for _, a := range g.Aliases {
			ak := normalizeKey(a)
			if strings.Contains(q, ak) || strings.Contains(ak, q) {
				return g, true
			}
		}
	}
	return gameEntry{}, false
}

func normalizeKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) || r == ':' || r == '!' || r == '·' || r == '-' || r == '_' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func utf8Len(s string) int {
	return len([]rune(s))
}

// iTunes Search API：https://performance-partners.apple.com/search-api
func (h *api) lookupITunes(term, artist string) (coverURL, trackName, artistName string, err error) {
	q := term
	if artist != "" {
		q = term + " " + artist
	}
	endpoint := "https://itunes.apple.com/search?" + url.Values{
		"term":   {q},
		"media":  {"music"},
		"entity": {"song"},
		"limit":  {"1"},
	}.Encode()
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("User-Agent", "NewtSpeak-ActivityCover/1.0")
	resp, err := h.client.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", "", io.EOF
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", "", err
	}
	var parsed struct {
		Results []struct {
			TrackName      string `json:"trackName"`
			ArtistName     string `json:"artistName"`
			ArtworkURL100  string `json:"artworkUrl100"`
			ArtworkURL60   string `json:"artworkUrl60"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", "", "", err
	}
	if len(parsed.Results) == 0 {
		return "", "", "", nil
	}
	r := parsed.Results[0]
	cover := r.ArtworkURL100
	if cover == "" {
		cover = r.ArtworkURL60
	}
	// 升到更大尺寸（iTunes 惯例：100x100 → 600x600）
	cover = strings.Replace(cover, "100x100bb", "600x600bb", 1)
	cover = strings.Replace(cover, "60x60bb", "600x600bb", 1)
	return cover, r.TrackName, r.ArtistName, nil
}
