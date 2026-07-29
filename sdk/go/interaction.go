package newtbot

import (
	"encoding/json"
	"time"
)

// ---------- 按钮交互（INTERACTION_CREATE） ----------

// InteractionMember 触发交互的成员信息。
type InteractionMember struct {
	UserID   string   `json:"user_id"`
	Username string   `json:"username"`
	Roles    []string `json:"roles"`
}

// Interaction 按钮点击交互（Gateway INTERACTION_CREATE 载荷包装）。
//
// 15 分钟内需用一次性 Token 回应：
//   - Ack 仅确认（用户按钮停止转圈）；之后仍可再 Reply/UpdateMessage 一次（defer 模式）
//   - Reply 以 bot 身份在原频道发新消息，缺省 ephemeral（仅点击者可见）
//   - UpdateMessage 更新原消息的 card 和/或 content
//
// 服务端错误：404（token 不符/非本 bot）、410 INTERACTION_EXPIRED、409 ALREADY_RESPONDED。
type Interaction struct {
	ID        string            `json:"id"`
	Token     string            `json:"token"`
	GuildID   string            `json:"guild_id"`
	ChannelID string            `json:"channel_id"`
	MessageID string            `json:"message_id"`
	CustomID  string            `json:"custom_id"`
	Member    InteractionMember `json:"member"`
	ExpiresAt time.Time         `json:"expires_at"`

	client *Client
}

// InteractionReplyOptions Reply 回应参数。
type InteractionReplyOptions struct {
	// Card 附加卡片，可为 nil。
	Card any
	// Ephemeral nil 时取服务端默认 true（仅点击者可见）。
	Ephemeral *bool
}

// interactionCallback POST /interactions/{id}/callback 请求体。
type interactionCallback struct {
	Token     string `json:"token"`
	Type      string `json:"type"` // ack | reply | update_message
	Content   string `json:"content,omitempty"`
	Card      any    `json:"card,omitempty"`
	Ephemeral *bool  `json:"ephemeral,omitempty"`
}

// ParseInteraction 把 INTERACTION_CREATE 事件载荷解析为可回应的 Interaction。
func (c *Client) ParseInteraction(payload json.RawMessage) (*Interaction, error) {
	var interaction Interaction
	if err := json.Unmarshal(payload, &interaction); err != nil {
		return nil, err
	}
	interaction.client = c
	return &interaction, nil
}

// Ack 仅确认，让用户按钮停止转圈；之后仍可再 Reply / UpdateMessage 一次。
func (i *Interaction) Ack() error {
	return i.client.request("POST", "/interactions/"+i.ID+"/callback",
		interactionCallback{Token: i.Token, Type: "ack"}, nil)
}

// Reply 以 bot 身份在原频道回复；options 零值即 ephemeral=true（服务端默认）。
// 返回新消息对象。
func (i *Interaction) Reply(content string, options InteractionReplyOptions) (*Message, error) {
	var out Message
	return &out, i.client.request("POST", "/interactions/"+i.ID+"/callback",
		interactionCallback{
			Token:     i.Token,
			Type:      "reply",
			Content:   content,
			Card:      options.Card,
			Ephemeral: options.Ephemeral,
		}, &out)
}

// ReplyText 纯文本 ephemeral 回复（语法糖）。
func (i *Interaction) ReplyText(content string) (*Message, error) {
	return i.Reply(content, InteractionReplyOptions{})
}

// UpdateMessage 更新原消息的 card 和/或 content；content 为空 / card 为 nil 时不改对应字段。
func (i *Interaction) UpdateMessage(content string, card any) error {
	return i.client.request("POST", "/interactions/"+i.ID+"/callback",
		interactionCallback{Token: i.Token, Type: "update_message", Content: content, Card: card}, nil)
}

// OnInteraction 注册按钮点击回调：INTERACTION_CREATE 载荷自动包装成 Interaction。
func (g *Gateway) OnInteraction(handler func(*Interaction)) *Gateway {
	return g.On("INTERACTION_CREATE", func(payload json.RawMessage) {
		interaction, err := g.client.ParseInteraction(payload)
		if err != nil {
			return
		}
		handler(interaction)
	})
}
