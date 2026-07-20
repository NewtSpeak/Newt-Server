# owlbot（Go SDK）

OwlSpeak 机器人开放平台官方 Go SDK。可搭配 [pion/webrtc](https://github.com/pion/webrtc)
实现完整的语音媒体收发（参考 Owl-SFU 仓库 `cmd/loadbot` 的信令时序）。

```bash
go get github.com/owlspeak/owl-server/sdk/go
```

## 快速上手

```go
package main

import (
	"encoding/json"
	"log"

	owlbot "github.com/owlspeak/owl-server/sdk/go"
)

func main() {
	bot := owlbot.New("https://owl.example.com", "owlbot_xxx")

	// 实时事件
	gw := bot.ConnectGateway()
	gw.On("MESSAGE_CREATE", func(payload json.RawMessage) {
		var msg owlbot.Message
		_ = json.Unmarshal(payload, &msg)
		if msg.AuthorIsBot {
			return // 防自我循环
		}

		// 流式回复（对接 LLM）
		_ = bot.Typing(msg.ChannelID)
		stream, err := bot.StartStream(msg.ChannelID, "")
		if err != nil {
			log.Println(err)
			return
		}
		for _, chunk := range []string{"思考中… ", "答案是 ", "42。"} {
			_ = stream.Append(chunk)
		}
		_, _ = stream.End(map[string]any{"footer": "AI Bot"})
	})

	// 卡片消息
	_, _ = bot.SendCard(channelID, map[string]any{
		"title": "部署完成", "color": "#22c55e",
		"fields": []map[string]any{{"name": "耗时", "value": "42s", "inline": true}},
	})

	select {}
}
```

## 语音接入

```go
// 机器人拥有独立音频权限（由其绑定角色决定），无需客户端或用户账号；
// 音频流中带 is_bot 独立标记。
voice, err := bot.JoinVoice(guildID, voiceChannelID)
// voice.Token           → Media Token（bot=true claim，TTL 2-5 分钟）
// voice.AdvertiseWSSURL → SFU 信令：auth → ready → offer/answer/ice → WebRTC Opus
// 周期调用 bot.RefreshVoiceToken(guildID)，把新 token 经信令 auth 帧在位续签

defer bot.LeaveVoice(guildID)
```

媒体层完整示例可直接参考 Owl-SFU 仓库的 `cmd/loadbot/main.go`（pion 建 PC、
发 Opus RTP、订阅下行轨），把 `--token` 换成 `JoinVoice` 返回的 Media Token 即可。

完整协议文档见 [`../README.md`](../README.md)。
