// Package owlbot 是 OwlSpeak 机器人开放平台的官方 Go SDK。
//
// 认证：Authorization: Bot <token>；基础地址自动拼接 /bot-api/v1。
// 语音媒体层可搭配 pion/webrtc 使用（Media Token 携带 bot=true claim，
// 机器人在音频流参与者信令中带 is_bot 独立标记）。
package owlbot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Error SDK 统一错误：携带 HTTP 状态码与服务端错误码。
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return e.Code
	}
	return fmt.Sprintf("请求失败（%d）", e.Status)
}

// Client OwlSpeak 机器人开放 API 客户端。
type Client struct {
	apiBase string
	token   string
	http    *http.Client
}

// New 创建客户端；baseURL 形如 https://owl.example.com。
func New(baseURL, token string) *Client {
	return &Client{
		apiBase: strings.TrimRight(baseURL, "/") + "/bot-api/v1",
		token:   token,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) request(method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequest(method, c.apiBase+path, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bot "+c.token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode >= 400 {
		var payload struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &payload)
		return &Error{Status: response.StatusCode, Code: payload.Error.Code, Message: payload.Error.Message}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// ---------- 类型 ----------

// Message 消息视图（服务端 messageView 的常用字段）。
type Message struct {
	ID             string          `json:"id"`
	GuildID        string          `json:"guild_id"`
	ChannelID      string          `json:"channel_id"`
	AuthorID       string          `json:"author_id"`
	AuthorUsername string          `json:"author_username"`
	AuthorIsBot    bool            `json:"author_is_bot"`
	Content        string          `json:"content"`
	Card           json.RawMessage `json:"card,omitempty"`
	StreamStatus   string          `json:"stream_status,omitempty"`
	ReplyToID      string          `json:"reply_to_id,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
}

// SendMessageOptions 发送消息参数。
type SendMessageOptions struct {
	Content       string   `json:"content,omitempty"`
	Card          any      `json:"card,omitempty"`
	ReplyToID     string   `json:"reply_to_id,omitempty"`
	AttachmentIDs []string `json:"attachment_ids,omitempty"`
	Nonce         string   `json:"nonce,omitempty"`
	// VisibleToUserIDs ephemeral 白名单（≤20 个 user_id）：
	// 带此字段即仅名单用户 + bot 自己可见，且不能带附件。
	VisibleToUserIDs []string `json:"visible_to_user_ids,omitempty"`
}

// VoiceJoinResult 语音进房结果。
type VoiceJoinResult struct {
	Token           string   `json:"token"`
	NodeID          string   `json:"node_id"`
	RoomID          string   `json:"room_id"`
	AdvertiseWSSURL string   `json:"advertise_wss_url"`
	Caps            []string `json:"caps"`
	SessionID       string   `json:"session_id"`
	ExpiresAt       int64    `json:"expires_at"`
}

// ---------- 基础资源 ----------

// Me 机器人档案与用户身份。
func (c *Client) Me() (map[string]any, error) {
	var out map[string]any
	return out, c.request("GET", "/me", nil, &out)
}

// Guilds 已安装的服务器列表。
func (c *Client) Guilds() ([]map[string]any, error) {
	var out struct {
		Guilds []map[string]any `json:"guilds"`
	}
	return out.Guilds, c.request("GET", "/guilds", nil, &out)
}

// Channels 可见频道列表。
func (c *Client) Channels(guildID string) ([]map[string]any, error) {
	var out struct {
		Channels []map[string]any `json:"channels"`
	}
	return out.Channels, c.request("GET", "/guilds/"+guildID+"/channels", nil, &out)
}

// Members 成员目录（含 is_bot 标记）。
func (c *Client) Members(guildID string) ([]map[string]any, error) {
	var out struct {
		Members []map[string]any `json:"members"`
	}
	return out.Members, c.request("GET", "/guilds/"+guildID+"/members", nil, &out)
}

// ---------- 消息 ----------

// SendMessage 发送消息（正文 / 卡片 / 回复 / 附件）。
func (c *Client) SendMessage(channelID string, options SendMessageOptions) (*Message, error) {
	if options.Nonce == "" {
		options.Nonce = uuid.NewString()
	}
	var out Message
	return &out, c.request("POST", "/channels/"+channelID+"/messages", options, &out)
}

// SendText 发送纯文本（语法糖）。
func (c *Client) SendText(channelID, content string) (*Message, error) {
	return c.SendMessage(channelID, SendMessageOptions{Content: content})
}

// SendCard 发送卡片消息（语法糖）。
func (c *Client) SendCard(channelID string, card any) (*Message, error) {
	return c.SendMessage(channelID, SendMessageOptions{Card: card})
}

// SendEphemeral 发送 ephemeral 消息（语法糖）：仅 userID 与 bot 自己可见；card 可为 nil。
func (c *Client) SendEphemeral(channelID, userID, content string, card any) (*Message, error) {
	return c.SendMessage(channelID, SendMessageOptions{
		Content:          content,
		Card:             card,
		VisibleToUserIDs: []string{userID},
	})
}

// GetMessages 拉取历史消息。
func (c *Client) GetMessages(channelID string, limit int, before string) ([]Message, error) {
	params := url.Values{}
	if limit > 0 {
		params.Set("limit", fmt.Sprint(limit))
	}
	if before != "" {
		params.Set("before", before)
	}
	path := "/channels/" + channelID + "/messages"
	if encoded := params.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out struct {
		Messages []Message `json:"messages"`
	}
	return out.Messages, c.request("GET", path, nil, &out)
}

// EditMessage 编辑消息正文。
func (c *Client) EditMessage(channelID, messageID, content string) (*Message, error) {
	var out Message
	return &out, c.request("PATCH", "/channels/"+channelID+"/messages/"+messageID,
		map[string]string{"content": content}, &out)
}

// DeleteMessage 删除消息。
func (c *Client) DeleteMessage(channelID, messageID string) error {
	return c.request("DELETE", "/channels/"+channelID+"/messages/"+messageID, nil, nil)
}

// AddReaction 添加表情反应。
func (c *Client) AddReaction(channelID, messageID, emoji string) error {
	return c.request("PUT", "/channels/"+channelID+"/messages/"+messageID+"/reactions/"+url.PathEscape(emoji)+"/@me", nil, nil)
}

// Typing 打字指示。
func (c *Client) Typing(channelID string) error {
	return c.request("POST", "/channels/"+channelID+"/typing", nil, nil)
}

// ---------- 流式消息 ----------

// MessageStream 流式消息句柄。
type MessageStream struct {
	client    *Client
	channelID string
	// ID 占位消息 ID。
	ID      string
	Message Message
	ended   bool
}

// StartStream 开始一条流式消息。
func (c *Client) StartStream(channelID, initialContent string) (*MessageStream, error) {
	var out Message
	err := c.request("POST", "/channels/"+channelID+"/messages/stream",
		map[string]string{"content": initialContent, "nonce": uuid.NewString()}, &out)
	if err != nil {
		return nil, err
	}
	return &MessageStream{client: c, channelID: channelID, ID: out.ID, Message: out}, nil
}

// Append 追加增量分片。
func (s *MessageStream) Append(delta string) error {
	if s.ended {
		return fmt.Errorf("流式消息已结束")
	}
	return s.client.request("POST", "/channels/"+s.channelID+"/messages/"+s.ID+"/stream",
		map[string]string{"delta": delta}, nil)
}

// End 结束流式消息；card 可为 nil。
func (s *MessageStream) End(card any) (*Message, error) {
	if s.ended {
		return &s.Message, nil
	}
	s.ended = true
	body := map[string]any{}
	if card != nil {
		body["card"] = card
	}
	var out Message
	if err := s.client.request("POST", "/channels/"+s.channelID+"/messages/"+s.ID+"/stream/end", body, &out); err != nil {
		return nil, err
	}
	s.Message = out
	return &out, nil
}

// ---------- 语音 ----------

// JoinVoice 加入语音频道：返回 Media Token（bot=true claim）与 SFU 信令地址。
// token TTL 2–5 分钟，需周期调用 RefreshVoiceToken 并经信令 auth 帧在位续签。
func (c *Client) JoinVoice(guildID, channelID string) (*VoiceJoinResult, error) {
	var out VoiceJoinResult
	return &out, c.request("POST", "/voice/join",
		map[string]string{"guild_id": guildID, "channel_id": channelID}, &out)
}

// LeaveVoice 离开语音频道。
func (c *Client) LeaveVoice(guildID string) error {
	return c.request("POST", "/voice/leave", map[string]string{"guild_id": guildID}, nil)
}

// RefreshVoiceToken 续签 Media Token。
func (c *Client) RefreshVoiceToken(guildID string) (token string, expiresAt int64, err error) {
	var out struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	err = c.request("POST", "/voice/refresh-token", map[string]string{"guild_id": guildID}, &out)
	return out.Token, out.ExpiresAt, err
}
