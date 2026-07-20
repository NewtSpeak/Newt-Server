"""OwlSpeak Bot SDK（Python）。

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

__all__ = ["OwlBotClient", "OwlBotError", "MessageStream", "run_gateway"]


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


class OwlBotClient:
    """OwlSpeak 机器人开放 API 客户端（同步 REST）。"""

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
        return self._request("GET", f"/guilds/{guild_id}/members").get("members", [])

    def permissions(self, guild_id: str) -> Dict[str, Any]:
        return self._request("GET", f"/guilds/{guild_id}/permissions/@me")

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
    ) -> Dict[str, Any]:
        return self._request(
            "POST",
            f"/channels/{channel_id}/messages",
            {
                "content": content or "",
                "card": card,
                "reply_to_id": reply_to_id or "",
                "attachment_ids": list(attachment_ids or []),
                "nonce": nonce or uuid.uuid4().hex,
            },
        )

    def send_card(self, channel_id: str, card: Dict[str, Any], **kwargs: Any) -> Dict[str, Any]:
        return self.send_message(channel_id, card=card, **kwargs)

    def get_messages(
        self, channel_id: str, *, before: Optional[str] = None, after: Optional[str] = None, limit: Optional[int] = None
    ) -> List[Dict[str, Any]]:
        params = {k: v for k, v in {"before": before, "after": after, "limit": limit}.items() if v}
        query = f"?{urllib.parse.urlencode(params)}" if params else ""
        return self._request("GET", f"/channels/{channel_id}/messages{query}").get("messages", [])

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
                        result = on_event(frame.get("t", ""), frame.get("d"))
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
