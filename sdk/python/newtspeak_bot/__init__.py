"""NewtSpeak Bot SDK（Python）。

REST 部分仅依赖标准库；Gateway 实时事件需可选依赖 ``websockets``
（``pip install owlspeak-bot[gateway]``）。

认证：``Authorization: Bot <token>``；基础地址自动拼接 ``/bot-api/v1``。
"""

from __future__ import annotations

import asyncio
import json
import uuid
import urllib.error
import urllib.parse
import urllib.request
from typing import Any, AsyncIterator, Callable, Dict, Iterable, List, Optional

__all__ = ["OwlBotClient", "OwlBotError", "MessageStream", "Interaction", "run_gateway"]


class OwlBotError(Exception):
    """SDK 统一错误：携带 HTTP 状态码与服务端错误码。"""

    def __init__(self, status: int, code: Optional[str], message: Optional[str]):
        super().__init__(message or code or f"请求失败（{status}）")
        self.status = status
        self.code = code


class MessageStream:
    """流式消息句柄：append 追加分片、end 收束（可带终态卡片）。"""

    def __init__(self, client: "OwlBotClient", channel_id: str, message: Dict[str, Any]):
        self._client = client
        self.channel_id = channel_id
        self.message = message
        self.id = str(message["id"])
        self.ended = False

    def append(self, delta: str) -> Dict[str, Any]:
        if self.ended:
            raise RuntimeError("流式消息已结束")
        return self._client._request(
            "POST", f"/channels/{self.channel_id}/messages/{self.id}/stream", {"delta": delta}
        )

    def end(self, content: Optional[str] = None, card: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
        if self.ended:
            return self.message
        self.ended = True
        body: Dict[str, Any] = {}
        if content is not None:
            body["content"] = content
        if card is not None:
            body["card"] = card
        self.message = self._client._request(
            "POST", f"/channels/{self.channel_id}/messages/{self.id}/stream/end", body
        )
        return self.message

    def __enter__(self) -> "MessageStream":
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        if not self.ended:
            self.end()


class Interaction:
    """按钮点击交互（Gateway ``INTERACTION_CREATE`` 载荷包装）。

    15 分钟内需用一次性 token 回应：

    - ``ack()`` 仅确认（用户按钮停止转圈）；之后仍可再 reply/update_message 一次（defer 模式）
    - ``reply()`` 以 bot 身份在原频道发新消息，缺省 ephemeral（仅点击者可见）
    - ``update_message()`` 更新原消息的 card 和/或 content

    服务端错误：404（token 不符/非本 bot）、410 INTERACTION_EXPIRED、409 ALREADY_RESPONDED。
    """

    def __init__(self, client: "OwlBotClient", payload: Dict[str, Any]):
        self._client = client
        self.raw = payload
        self.id = str(payload.get("id", ""))
        self.token = payload.get("token", "")
        self.guild_id = payload.get("guild_id", "")
        self.channel_id = payload.get("channel_id", "")
        self.message_id = str(payload.get("message_id", ""))
        self.custom_id = payload.get("custom_id", "")
        self.member = payload.get("member") or {}
        self.expires_at = payload.get("expires_at", "")

    def _callback(self, body: Dict[str, Any]) -> Any:
        body["token"] = self.token
        return self._client._request("POST", f"/interactions/{self.id}/callback", body)

    def ack(self) -> None:
        """仅确认，让用户按钮停止转圈；之后仍可再 reply / update_message 一次。"""
        self._callback({"type": "ack"})

    def reply(
        self, content: Optional[str] = None, *, card: Optional[Dict[str, Any]] = None, ephemeral: bool = True
    ) -> Dict[str, Any]:
        """以 bot 身份在原频道回复；ephemeral 缺省 True（仅点击者可见）。返回新消息对象。"""
        body: Dict[str, Any] = {"type": "reply", "ephemeral": ephemeral}
        if content is not None:
            body["content"] = content
        if card is not None:
            body["card"] = card
        return self._callback(body)

    def update_message(
        self, content: Optional[str] = None, *, card: Optional[Dict[str, Any]] = None
    ) -> Any:
        """更新原消息的 card 和/或 content。"""
        body: Dict[str, Any] = {"type": "update_message"}
        if content is not None:
            body["content"] = content
        if card is not None:
            body["card"] = card
        return self._callback(body)


class OwlBotClient:
    """NewtSpeak 机器人开放 API 客户端（同步 REST）。"""

    def __init__(self, base_url: str, token: str, timeout: float = 15.0):
        if not base_url or not token:
            raise ValueError("base_url 与 token 必填")
        self.api_base = base_url.rstrip("/") + "/bot-api/v1"
        self.token = token
        self.timeout = timeout

    # ---------- 内部请求 ----------

    def _request(self, method: str, path: str, body: Optional[Dict[str, Any]] = None) -> Any:
        data = json.dumps(body).encode() if body is not None else None
        request = urllib.request.Request(self.api_base + path, data=data, method=method)
        request.add_header("Authorization", f"Bot {self.token}")
        if data is not None:
            request.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(request, timeout=self.timeout) as response:
                if response.status == 204:
                    return None
                return json.loads(response.read() or b"{}")
        except urllib.error.HTTPError as exc:
            try:
                payload = json.loads(exc.read() or b"{}")
            except Exception:
                payload = {}
            error = payload.get("error") or {}
            raise OwlBotError(exc.code, error.get("code"), error.get("message")) from None

    # ---------- 基础资源 ----------

    def me(self) -> Dict[str, Any]:
        return self._request("GET", "/me")

    def guilds(self) -> List[Dict[str, Any]]:
        return self._request("GET", "/guilds").get("guilds", [])

    def channels(self, guild_id: str) -> List[Dict[str, Any]]:
        return self._request("GET", f"/guilds/{guild_id}/channels").get("channels", [])

    def members(self, guild_id: str) -> List[Dict[str, Any]]:
        """成员目录（含 is_bot、is_owner、role_ids）。"""
        return self._request("GET", f"/guilds/{guild_id}/members").get("members", [])

    def member(self, guild_id: str, member_id: str) -> Dict[str, Any]:
        """单成员详情；member_id 可为 members.id 或 user_id。"""
        return self._request("GET", f"/guilds/{guild_id}/members/{member_id}")

    def permissions(self, guild_id: str) -> Dict[str, Any]:
        return self._request("GET", f"/guilds/{guild_id}/permissions/@me")

    def channel_permissions(self, guild_id: str, channel_id: str) -> Dict[str, Any]:
        return self._request("GET", f"/guilds/{guild_id}/channels/{channel_id}/permissions/@me")

    # ---------- 角色与成员角色（反应角色 / 验证门） ----------

    def roles(self, guild_id: str) -> List[Dict[str, Any]]:
        result = self._request("GET", f"/guilds/{guild_id}/roles")
        return result if isinstance(result, list) else []

    def role(self, guild_id: str, role_id: str) -> Dict[str, Any]:
        return self._request("GET", f"/guilds/{guild_id}/roles/{role_id}")

    def create_role(
        self,
        guild_id: str,
        name: str,
        *,
        permissions: int = 0,
        position: int = 1,
        color: Optional[str] = None,
        hoist: Optional[bool] = None,
        mentionable: Optional[bool] = None,
    ) -> Dict[str, Any]:
        body: Dict[str, Any] = {"name": name, "permissions": permissions, "position": position}
        if color is not None:
            body["color"] = color
        if hoist is not None:
            body["hoist"] = hoist
        if mentionable is not None:
            body["mentionable"] = mentionable
        return self._request("POST", f"/guilds/{guild_id}/roles", body)

    def update_role(
        self,
        guild_id: str,
        role_id: str,
        name: str,
        *,
        permissions: int = 0,
        position: int = 1,
        color: Optional[str] = None,
        hoist: Optional[bool] = None,
        mentionable: Optional[bool] = None,
    ) -> Dict[str, Any]:
        body: Dict[str, Any] = {"name": name, "permissions": permissions, "position": position}
        if color is not None:
            body["color"] = color
        if hoist is not None:
            body["hoist"] = hoist
        if mentionable is not None:
            body["mentionable"] = mentionable
        return self._request("PATCH", f"/guilds/{guild_id}/roles/{role_id}", body)

    def delete_role(self, guild_id: str, role_id: str) -> None:
        self._request("DELETE", f"/guilds/{guild_id}/roles/{role_id}")

    def add_member_role(self, guild_id: str, member_id: str, role_id: str) -> Dict[str, Any]:
        """给成员绑定角色；member_id 接受 members.id 或 user_id。"""
        return self._request("PUT", f"/guilds/{guild_id}/members/{member_id}/roles/{role_id}")

    def remove_member_role(self, guild_id: str, member_id: str, role_id: str) -> None:
        self._request("DELETE", f"/guilds/{guild_id}/members/{member_id}/roles/{role_id}")

    # ---------- 频道权限覆盖（验证门搭建） ----------

    def overwrites(self, guild_id: str, channel_id: str) -> List[Dict[str, Any]]:
        result = self._request("GET", f"/guilds/{guild_id}/channels/{channel_id}/overwrites")
        return result if isinstance(result, list) else []

    def set_overwrite(
        self,
        guild_id: str,
        channel_id: str,
        target_id: str,
        *,
        type: str,
        allow: int = 0,
        deny: int = 0,
    ) -> Dict[str, Any]:
        return self._request(
            "PUT",
            f"/guilds/{guild_id}/channels/{channel_id}/overwrites/{target_id}",
            {"type": type, "allow": allow, "deny": deny},
        )

    def delete_overwrite(
        self, guild_id: str, channel_id: str, target_id: str, *, type: Optional[str] = None
    ) -> None:
        query = f"?type={urllib.parse.quote(type)}" if type else ""
        self._request("DELETE", f"/guilds/{guild_id}/channels/{channel_id}/overwrites/{target_id}{query}")

    # ---------- 服务器 / 频道结构 ----------

    def guild(self, guild_id: str) -> Dict[str, Any]:
        return self._request("GET", f"/guilds/{guild_id}")

    def update_guild(self, guild_id: str, **body: Any) -> Dict[str, Any]:
        return self._request("PATCH", f"/guilds/{guild_id}", body)

    def channel(self, channel_id: str) -> Dict[str, Any]:
        return self._request("GET", f"/channels/{channel_id}")

    def create_channel(self, guild_id: str, **body: Any) -> Dict[str, Any]:
        return self._request("POST", f"/guilds/{guild_id}/channels", body)

    def update_channel(self, channel_id: str, **body: Any) -> Dict[str, Any]:
        return self._request("PATCH", f"/channels/{channel_id}", body)

    def delete_channel(self, channel_id: str) -> None:
        self._request("DELETE", f"/channels/{channel_id}")

    # ---------- 成员治理 ----------

    def kick_member(self, guild_id: str, member_id: str) -> None:
        """踢出成员；member_id=@me 时为 bot 主动退服。"""
        self._request("DELETE", f"/guilds/{guild_id}/members/{member_id}")

    def leave_guild(self, guild_id: str) -> None:
        self.kick_member(guild_id, "@me")

    def update_member(self, guild_id: str, member_id: str, **body: Any) -> Dict[str, Any]:
        return self._request("PATCH", f"/guilds/{guild_id}/members/{member_id}", body)

    def ban_user(self, guild_id: str, user_id: str, *, reason: Optional[str] = None) -> None:
        body: Dict[str, Any] = {}
        if reason is not None:
            body["reason"] = reason
        self._request("PUT", f"/guilds/{guild_id}/bans/{user_id}", body)

    def unban_user(self, guild_id: str, user_id: str) -> None:
        self._request("DELETE", f"/guilds/{guild_id}/bans/{user_id}")

    def bans(self, guild_id: str) -> Any:
        return self._request("GET", f"/guilds/{guild_id}/bans")

    def create_invite(
        self, guild_id: str, *, ttl_seconds: Optional[int] = None, max_uses: Optional[int] = None
    ) -> Dict[str, Any]:
        body: Dict[str, Any] = {}
        if ttl_seconds:
            body["ttl_seconds"] = ttl_seconds
        if max_uses:
            body["max_uses"] = max_uses
        return self._request("POST", f"/guilds/{guild_id}/invites", body)

    def invites(self, guild_id: str) -> Any:
        return self._request("GET", f"/guilds/{guild_id}/invites")

    def delete_invite(self, code: str) -> None:
        self._request("DELETE", f"/invites/{code}")

    # ---------- Restriction / 审计 / 语音管理 ----------

    def create_restriction(self, guild_id: str, body: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", f"/guilds/{guild_id}/restrictions", body)

    def restrictions(self, guild_id: str) -> Any:
        return self._request("GET", f"/guilds/{guild_id}/restrictions")

    def lift_restriction(self, guild_id: str, restriction_id: str) -> None:
        self._request("DELETE", f"/guilds/{guild_id}/restrictions/{restriction_id}")

    def audit_logs(self, guild_id: str) -> Any:
        return self._request("GET", f"/guilds/{guild_id}/audit-logs")

    def voice_disconnect(self, guild_id: str, body: Dict[str, Any]) -> None:
        self._request("POST", f"/guilds/{guild_id}/voice/disconnect", body)

    def voice_move(self, guild_id: str, body: Dict[str, Any]) -> None:
        self._request("POST", f"/guilds/{guild_id}/voice/move", body)

    def update_voice_state(self, guild_id: str, user_id: str, body: Dict[str, Any]) -> None:
        self._request("PATCH", f"/guilds/{guild_id}/voice/states/{user_id}", body)

    # ---------- 消息 ----------

    def send_message(
        self,
        channel_id: str,
        content: Optional[str] = None,
        *,
        card: Optional[Dict[str, Any]] = None,
        reply_to_id: Optional[str] = None,
        attachment_ids: Optional[Iterable[str]] = None,
        nonce: Optional[str] = None,
        visible_to_user_ids: Optional[Iterable[str]] = None,
    ) -> Dict[str, Any]:
        """发送消息。

        visible_to_user_ids：ephemeral 白名单（≤20 个 user_id）；
        带此字段即仅名单用户 + bot 自己可见，且不能带附件。
        """
        body: Dict[str, Any] = {
            "content": content or "",
            "card": card,
            "reply_to_id": reply_to_id or "",
            "attachment_ids": list(attachment_ids or []),
            "nonce": nonce or uuid.uuid4().hex,
        }
        if visible_to_user_ids is not None:
            body["visible_to_user_ids"] = list(visible_to_user_ids)
        return self._request("POST", f"/channels/{channel_id}/messages", body)

    def send_card(self, channel_id: str, card: Dict[str, Any], **kwargs: Any) -> Dict[str, Any]:
        return self.send_message(channel_id, card=card, **kwargs)

    def send_ephemeral(
        self, channel_id: str, user_id: str, content: Optional[str] = None, *, card: Optional[Dict[str, Any]] = None
    ) -> Dict[str, Any]:
        """发送 ephemeral 消息（语法糖）：仅 user_id 与 bot 自己可见。"""
        return self.send_message(channel_id, content, card=card, visible_to_user_ids=[user_id])

    def get_messages(
        self, channel_id: str, *, before: Optional[str] = None, after: Optional[str] = None, limit: Optional[int] = None
    ) -> List[Dict[str, Any]]:
        params = {k: v for k, v in {"before": before, "after": after, "limit": limit}.items() if v}
        query = f"?{urllib.parse.urlencode(params)}" if params else ""
        return self._request("GET", f"/channels/{channel_id}/messages{query}").get("messages", [])

    def get_message(self, channel_id: str, message_id: str) -> Dict[str, Any]:
        return self._request("GET", f"/channels/{channel_id}/messages/{message_id}")

    def edit_message(self, channel_id: str, message_id: str, content: str) -> Dict[str, Any]:
        return self._request("PATCH", f"/channels/{channel_id}/messages/{message_id}", {"content": content})

    def delete_message(self, channel_id: str, message_id: str) -> None:
        self._request("DELETE", f"/channels/{channel_id}/messages/{message_id}")

    def add_reaction(self, channel_id: str, message_id: str, emoji: str) -> None:
        self._request("PUT", f"/channels/{channel_id}/messages/{message_id}/reactions/{urllib.parse.quote(emoji)}/@me")

    def remove_reaction(self, channel_id: str, message_id: str, emoji: str) -> None:
        self._request(
            "DELETE", f"/channels/{channel_id}/messages/{message_id}/reactions/{urllib.parse.quote(emoji)}/@me"
        )

    def list_reaction_users(self, channel_id: str, message_id: str, emoji: str) -> List[Dict[str, Any]]:
        return self._request(
            "GET", f"/channels/{channel_id}/messages/{message_id}/reactions/{urllib.parse.quote(emoji)}"
        ).get("users", [])

    def typing(self, channel_id: str) -> None:
        self._request("POST", f"/channels/{channel_id}/typing")

    def search_messages(
        self, query: str, *, guild_id: Optional[str] = None, channel_id: Optional[str] = None, limit: Optional[int] = None
    ) -> List[Dict[str, Any]]:
        params = {"q": query}
        if guild_id:
            params["guild_id"] = guild_id
        if channel_id:
            params["channel_id"] = channel_id
        if limit:
            params["limit"] = str(limit)
        return self._request("GET", f"/search/messages?{urllib.parse.urlencode(params)}").get("messages", [])

    # ---------- 流式消息 ----------

    def start_stream(
        self, channel_id: str, *, content: Optional[str] = None, reply_to_id: Optional[str] = None
    ) -> MessageStream:
        message = self._request(
            "POST",
            f"/channels/{channel_id}/messages/stream",
            {"content": content or "", "reply_to_id": reply_to_id or "", "nonce": uuid.uuid4().hex},
        )
        return MessageStream(self, channel_id, message)

    # ---------- 语音 ----------

    def join_voice(
        self, guild_id: str, channel_id: str, *, self_mute: bool = False, self_deaf: bool = False
    ) -> Dict[str, Any]:
        """加入语音频道。返回含 Media Token（bot=true claim）、SFU 信令地址、caps 等。

        机器人拥有独立音频权限（由其绑定角色决定），并在音频流参与者信令中
        带 is_bot 独立标记。token TTL 2-5 分钟，需周期调用 refresh_voice_token。
        """
        return self._request(
            "POST",
            "/voice/join",
            {"guild_id": guild_id, "channel_id": channel_id, "self_mute": self_mute, "self_deaf": self_deaf},
        )

    def leave_voice(self, guild_id: str) -> Dict[str, Any]:
        return self._request("POST", "/voice/leave", {"guild_id": guild_id})

    def refresh_voice_token(self, guild_id: str) -> Dict[str, Any]:
        return self._request("POST", "/voice/refresh-token", {"guild_id": guild_id})

    def voice_states(self, guild_id: str, channel_id: str) -> List[Dict[str, Any]]:
        return self._request("GET", f"/guilds/{guild_id}/channels/{channel_id}/voice-states").get("voice_states", [])

    # ---------- Gateway ----------

    @property
    def gateway_url(self) -> str:
        scheme = "wss" if self.api_base.startswith("https") else "ws"
        return scheme + self.api_base[self.api_base.index("://"):] + "/gateway"


async def run_gateway(
    client: OwlBotClient,
    on_event: Callable[[str, Any], Any],
    *,
    reconnect: bool = True,
) -> None:
    """连接 Gateway 并把 DISPATCH 事件回调给 on_event(event_type, payload)。

    需要可选依赖 websockets（pip install owlspeak-bot[gateway]）。
    on_event 可为普通函数或协程；内置事件 "READY" 也会回调。
    按钮点击（INTERACTION_CREATE）除原始事件外，还会以包装事件
    ``on_event("interaction", Interaction)`` 再次回调，可直接
    ``interaction.ack()`` / ``reply()`` / ``update_message()``。
    """
    try:
        import websockets
    except ImportError as exc:  # pragma: no cover
        raise RuntimeError("Gateway 需要 websockets：pip install owlspeak-bot[gateway]") from exc

    backoff = 1.0
    while True:
        try:
            async with websockets.connect(client.gateway_url) as ws:
                heartbeat_task: Optional[asyncio.Task] = None
                async for raw in ws:
                    frame = json.loads(raw)
                    op = frame.get("op")
                    if op == "HELLO":
                        await ws.send(json.dumps({"op": "IDENTIFY", "d": {"token": client.token}}))
                        interval = (frame.get("d") or {}).get("heartbeat_interval_ms", 30000) / 1000

                        async def beat() -> None:
                            while True:
                                await asyncio.sleep(interval)
                                await ws.send(json.dumps({"op": "HEARTBEAT"}))

                        heartbeat_task = asyncio.create_task(beat())
                    elif op == "READY":
                        backoff = 1.0
                        result = on_event("READY", frame.get("d"))
                        if asyncio.iscoroutine(result):
                            await result
                    elif op == "DISPATCH":
                        event_type = frame.get("t", "")
                        payload = frame.get("d")
                        result = on_event(event_type, payload)
                        if asyncio.iscoroutine(result):
                            await result
                        if event_type == "INTERACTION_CREATE" and isinstance(payload, dict):
                            result = on_event("interaction", Interaction(client, payload))
                            if asyncio.iscoroutine(result):
                                await result
                if heartbeat_task:
                    heartbeat_task.cancel()
        except asyncio.CancelledError:
            raise
        except Exception:
            if not reconnect:
                raise
        if not reconnect:
            return
        await asyncio.sleep(backoff)
        backoff = min(backoff * 2, 30.0)
