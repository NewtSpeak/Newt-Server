# newtspeak-bot（Python SDK）

NewtSpeak 机器人开放平台官方 Python SDK。REST 仅用标准库；Gateway 实时事件需可选依赖：

```bash
pip install newtspeak-bot            # REST
pip install "newtspeak-bot[gateway]" # REST + Gateway（websockets）
```

## 快速上手

```python
import asyncio
from newtspeak_bot import OwlBotClient, run_gateway

bot = OwlBotClient("https://newt.example.com", token="newtbot_xxx")

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

## 按钮交互与 ephemeral

```python
from newtspeak_bot import Interaction

# 1. 发带交互按钮的卡片（custom_id 触发 INTERACTION_CREATE；url 为链接按钮）
bot.send_card(channel_id, {
    "title": "发布 v1.4.2？",
    "buttons": [
        {"label": "批准", "custom_id": "approve:42", "style": "success"},
        {"label": "查看日志", "url": "https://ci.example.com/run/42"},
    ],
})

# 2. ephemeral 消息：仅指定用户 + bot 自己可见
bot.send_ephemeral(channel_id, user_id, "只有你能看到这条提示")

# 3. 处理按钮点击（15 分钟内回应；reply 缺省 ephemeral=True）
async def on_event(event, data):
    if event == "interaction":  # INTERACTION_CREATE 的 Interaction 包装
        interaction: Interaction = data
        if interaction.custom_id == "approve:42":
            interaction.update_message(card={"title": "已批准 ✅"})
        else:
            interaction.reply("已驳回")  # 仅点击者可见
        # 耗时任务可先 interaction.ack()，稍后再 reply / update_message 一次（defer 模式）

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

## 反应角色（验证门）

```python
RULES_MESSAGE_ID = "..."
VERIFIED_ROLE_ID = "..."

async def on_event(event, data):
    if event == "MESSAGE_REACTION_ADD":
        if str(data.get("message_id")) != RULES_MESSAGE_ID or data.get("emoji") != "✅":
            return
        # user_id 可直接作为 members/{id} 路径（服务端同时接受 member_id / user_id）
        bot.add_member_role(data["guild_id"], data["user_id"], VERIFIED_ROLE_ID)
    elif event == "MESSAGE_REACTION_REMOVE":
        if str(data.get("message_id")) != RULES_MESSAGE_ID or data.get("emoji") != "✅":
            return
        try:
            bot.remove_member_role(data["guild_id"], data["user_id"], VERIFIED_ROLE_ID)
        except Exception:
            pass

asyncio.run(run_gateway(bot, on_event))
```

相关 API：`roles` / `create_role` / `add_member_role` / `remove_member_role` / `member` /
`set_overwrite` / `list_reaction_users`。安装后需给 bot 绑定带 `MANAGE_ROLES` 的角色。

完整协议文档见 [`../README.md`](../README.md)。
