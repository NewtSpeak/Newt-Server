import { useEffect, useRef } from "react"

import { gatewayURL, getSession } from "~/lib/api"

// WS /api/v1/gateway 实时通道。
// 帧格式：{"op":"HELLO"|"IDENTIFY"|"READY"|"HEARTBEAT"|"HEARTBEAT_ACK"|"DISPATCH", ...}
// 后端未就绪时静默失败并指数退避重连，不打扰页面（页面自身仍有拉取兜底）。

type GatewayFrame = {
  op: string
  t?: string
  d?: unknown
}

type Listener = (payload: unknown, eventName: string) => void

class GatewayClient {
  private socket: WebSocket | null = null
  private listeners = new Map<string, Set<Listener>>()
  private heartbeatTimer: ReturnType<typeof setInterval> | null = null
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null
  private attempts = 0
  private started = false

  subscribe(event: string, listener: Listener) {
    let set = this.listeners.get(event)
    if (!set) {
      set = new Set()
      this.listeners.set(event, set)
    }
    set.add(listener)
    this.ensureStarted()
    return () => {
      set.delete(listener)
      if (set.size === 0) this.listeners.delete(event)
    }
  }

  private ensureStarted() {
    if (this.started || typeof window === "undefined") return
    this.started = true
    this.connect()
  }

  private connect() {
    const session = getSession()
    if (!session) {
      this.scheduleReconnect()
      return
    }
    let socket: WebSocket
    try {
      socket = new WebSocket(gatewayURL())
    } catch {
      this.scheduleReconnect()
      return
    }
    this.socket = socket

    socket.onmessage = event => {
      let frame: GatewayFrame
      try {
        frame = JSON.parse(String(event.data)) as GatewayFrame
      } catch {
        return
      }
      switch (frame.op) {
        case "HELLO": {
          const interval = (frame.d as { heartbeat_interval?: number } | undefined)?.heartbeat_interval ?? 30_000
          const current = getSession()
          if (current) socket.send(JSON.stringify({ op: "IDENTIFY", d: { token: current.access_token } }))
          this.startHeartbeat(socket, interval)
          break
        }
        case "READY":
          this.attempts = 0
          break
        case "DISPATCH":
          if (frame.t) this.emit(frame.t, frame.d)
          break
      }
    }
    socket.onclose = () => {
      if (this.socket === socket) {
        this.socket = null
        this.stopHeartbeat()
        this.scheduleReconnect()
      }
    }
    socket.onerror = () => {
      socket.close()
    }
  }

  private startHeartbeat(socket: WebSocket, interval: number) {
    this.stopHeartbeat()
    this.heartbeatTimer = setInterval(() => {
      if (socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify({ op: "HEARTBEAT" }))
    }, Math.max(5_000, interval))
  }

  private stopHeartbeat() {
    if (this.heartbeatTimer) {
      clearInterval(this.heartbeatTimer)
      this.heartbeatTimer = null
    }
  }

  private scheduleReconnect() {
    if (this.reconnectTimer || this.listeners.size === 0) return
    const delay = Math.min(30_000, 1_000 * 2 ** this.attempts)
    this.attempts = Math.min(this.attempts + 1, 5)
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null
      this.connect()
    }, delay)
  }

  private emit(event: string, payload: unknown) {
    this.listeners.get(event)?.forEach(listener => listener(payload, event))
    this.listeners.get("*")?.forEach(listener => listener(payload, event))
  }
}

let client: GatewayClient | null = null

function getClient() {
  client ??= new GatewayClient()
  return client
}

/**
 * 订阅 Gateway DISPATCH 事件（如 VOICE_STATE_UPDATE、STAGE_QUEUE_UPDATE、RESTRICTION_CREATE）。
 * 后端未就绪时静默降级，handler 不会被调用。
 */
export function useGatewayEvent(events: string | string[], handler: (payload: unknown, eventName: string) => void) {
  const handlerRef = useRef(handler)
  handlerRef.current = handler
  const key = Array.isArray(events) ? events.join(",") : events

  useEffect(() => {
    const names = key.split(",").filter(Boolean)
    const unsubscribes = names.map(name => getClient().subscribe(name, (payload, eventName) => handlerRef.current(payload, eventName)))
    return () => unsubscribes.forEach(unsubscribe => unsubscribe())
  }, [key])
}
