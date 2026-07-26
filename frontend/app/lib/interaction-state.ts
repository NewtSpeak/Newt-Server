// bot 卡片交互按钮状态（设计文档 2026-07-26）：按 (messageId, customId) 键控。
// 控制台无 zustand，用模块级 Map + listeners + useSyncExternalStore 实现 mini-store。
//
// 点击流程：乐观 pending（按钮立即转圈）→ POST 202 回填 interaction_id 并武装
// 20s 本地超时 → INTERACTION_ACK 终态（ACKED 保持 disabled 显示 √ / RESPONDED √
// 定格 900ms 后回收 / EXPIRED 提示后回收）；REST 失败立即回收 + 错误码映射 toast。
// MESSAGE_UPDATE 换卡不清 entry：新卡同 custom_id 的按钮无缝延续 pending。
// SSR 安全：模块顶层不触碰 window，定时器仅由用户事件 / Gateway 回调触发。

import { useSyncExternalStore } from "react"
import { toast } from "sonner"

import { ApiError, createInteraction } from "~/lib/api"

export type InteractionStatus = "pending" | "acked" | "responded"

export type InteractionEntry = {
  key: string
  channelId: string
  messageId: string
  customId: string
  /** 202 受理后回填；ACK 到达时用于一致性校验（不匹配的迟到 ACK 忽略） */
  interactionId?: string
  status: InteractionStatus
  startedAt: number
}

/** Gateway INTERACTION_ACK 载荷（定向推给点击者） */
export type InteractionAckPayload = {
  interaction_id?: string
  message_id?: string
  custom_id?: string
  status?: "ACKED" | "RESPONDED" | "EXPIRED" | string
  event_at?: string
}

/** bot 无响应的本地兜底超时（服务端过期太久，UI 层 20s 即恢复可点） */
const LOCAL_TIMEOUT_MS = 20_000
/** responded 终态 √ 的定格时长，之后回收 entry 恢复 idle */
const RESPONDED_LINGER_MS = 900
/** 全局最小点击间隔（对齐服务端每用户 2 QPS 限流，防扫射不同按钮触发 429） */
const MIN_CLICK_INTERVAL_MS = 500

const entries = new Map<string, InteractionEntry>()
const listeners = new Set<() => void>()
// 定时器不进 entry（回收时统一清理）
const timers = new Map<string, ReturnType<typeof setTimeout>>()
let lastClickAt = 0

function interactionKey(messageId: string, customId: string) {
  return `${messageId} ${customId}`
}

function emit() {
  listeners.forEach((listener) => listener())
}

function clearTimer(key: string) {
  const timer = timers.get(key)
  if (timer) {
    clearTimeout(timer)
    timers.delete(key)
  }
}

function dropEntry(key: string) {
  clearTimer(key)
  if (entries.delete(key)) emit()
}

function patchEntry(key: string, patch: Partial<InteractionEntry>) {
  const entry = entries.get(key)
  if (!entry) return
  entries.set(key, { ...entry, ...patch })
  emit()
}

function scheduleDrop(key: string, delay: number) {
  clearTimer(key)
  timers.set(
    key,
    setTimeout(() => {
      timers.delete(key)
      dropEntry(key)
    }, delay)
  )
}

/** 错误码 → 提示文案（与服务端交互端点错误约定对齐） */
function toastForError(error: unknown) {
  const status = error instanceof ApiError ? error.status : 0
  if (status === 429) toast.error("操作太频繁，请稍候再试")
  else if (status === 400) toast.error("该按钮当前不可用")
  else if (status === 404) toast.error("消息或按钮已不可用")
  else if (status === 403) toast.error("没有使用交互组件的权限")
  else toast.error("网络异常，请重试")
}

/** 点击交互按钮（pending / 终态展示期重复点击自动忽略） */
export async function clickInteraction(
  channelId: string,
  messageId: string,
  customId: string
) {
  const key = interactionKey(messageId, customId)
  if (entries.has(key)) return
  const now = Date.now()
  if (now - lastClickAt < MIN_CLICK_INTERVAL_MS) return
  lastClickAt = now

  entries.set(key, {
    key,
    channelId,
    messageId,
    customId,
    status: "pending",
    startedAt: now,
  })
  emit()
  try {
    const result = await createInteraction(channelId, messageId, customId)
    // REST 返回前 ACK 可能已先到（WS 更快）：entry 已被推进/回收则不再武装超时
    const entry = entries.get(key)
    if (!entry) return
    patchEntry(key, { interactionId: result.interaction_id })
    if (entry.status === "pending") {
      // 20s 无 ACK：判定 bot 无响应，恢复可点并提示
      clearTimer(key)
      timers.set(
        key,
        setTimeout(() => {
          timers.delete(key)
          const current = entries.get(key)
          if (!current || current.status !== "pending") return
          dropEntry(key)
          toast.error("机器人暂无响应，请稍后重试")
        }, LOCAL_TIMEOUT_MS)
      )
    }
  } catch (reason) {
    // 失败：立即回收恢复可点 + 映射提示
    dropEntry(key)
    toastForError(reason)
  }
}

/** Gateway INTERACTION_ACK 入口（由 MessageCard 订阅转发） */
export function applyInteractionAck(payload: InteractionAckPayload) {
  if (!payload?.message_id || !payload.custom_id) return
  const key = interactionKey(payload.message_id, payload.custom_id)
  const entry = entries.get(key)
  if (!entry) return
  // interaction_id 不匹配 = 迟到的旧交互回执，忽略
  if (
    entry.interactionId &&
    payload.interaction_id &&
    entry.interactionId !== payload.interaction_id
  )
    return
  clearTimer(key)
  switch (payload.status) {
    case "ACKED":
      // 已确认仍待最终回应：保持 disabled 显示 √，并继续武装本地超时（到点静默回收）
      patchEntry(key, { status: "acked" })
      scheduleDrop(key, LOCAL_TIMEOUT_MS)
      break
    case "RESPONDED":
      patchEntry(key, { status: "responded" })
      scheduleDrop(key, RESPONDED_LINGER_MS)
      break
    case "EXPIRED":
      dropEntry(key)
      toast.error("交互已过期，请重新点击")
      break
  }
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

/** 订阅单个按钮的交互 entry（无进行中交互返回 undefined；SSR 快照恒为 undefined） */
export function useInteractionEntry(
  messageId: string,
  customId: string
): InteractionEntry | undefined {
  return useSyncExternalStore(
    subscribe,
    () => entries.get(interactionKey(messageId, customId)),
    () => undefined
  )
}
