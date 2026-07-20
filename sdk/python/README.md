# owlspeak-bot（Python SDK）

OwlSpeak 机器人开放平台官方 Python SDK。REST 仅用标准库；Gateway 实时事件需可选依赖：

```bash
pip install owlspeak-bot            # REST
pip install "owlspeak-bot[gateway]" # REST + Gateway（websockets）
```

## 快速上手

```python
import asyncio
from owlspeak_bot import OwlBotClient, run_gateway

bot = OwlBotClient("https://owl.example.com", token="owlbot_xxx")

# 普通消息 / 卡片消息
bot.send_message(channel_id, "你好！")
bot.send_card(channel_id, {
    "title": "部署完成",
    "description": "v1.4.2 已发布",
    "color": "#22c55e",
    "fields": [{"name": "耗时", "value": "42s", "inline": True}],
})

# 流式回复（对接 LLM）
bot.typing(channel_id)
with bot.start_stream(channel_id) as stream:
    for chunk in ask_llm("你好"):
        stream.append(chunk)
    stream.end(card={"footer": "AI Bot"})

# 实时事件
async def on_event(event, data):
    if event == "MESSAGE_CREATE" and not data.get("author_is_bot"):
        print("收到消息：", data["content"])

asyncio.run(run_gateway(bot, on_event))
```

## 语音接入

```python
# 机器人拥有独立音频权限（由其绑定角色决定），无需客户端或用户账号；
# 音频流中带 is_bot 独立标记。
voice = bot.join_voice(guild_id, voice_channel_id)
# voice["token"]             → Media Token（bot=true claim，TTL 2-5 分钟）
# voice["advertise_wss_url"] → SFU 信令：auth → ready → offer/answer/ice → WebRTC Opus
# 周期调用 bot.refresh_voice_token(guild_id)，把新 token 经信令 auth 帧在位续签

bot.leave_voice(guild_id)
```

媒体层可搭配 [aiortc](https://github.com/aiortc/aiortc) 实现音频收发。

完整协议文档见 [`../README.md`](../README.md)。
