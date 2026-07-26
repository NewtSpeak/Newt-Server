import { useEffect, useState } from "react"

import { getAvatarFrames, type EquippedSlotView } from "~/lib/api"

/**
 * 模块级缓存：userId -> 头像框（null = 已确认未装备）。
 * 跨组件/翻页共享，避免同一用户重复请求。
 */
const frameCache = new Map<string, EquippedSlotView | null>()

/** 进行中的批量请求：userId -> Promise，并发挂载的组件共享同一次请求 */
const inflight = new Map<string, Promise<void>>()

/**
 * 批量查询并缓存用户头像框。
 * - 过滤掉缓存已有的 id，剩余 id 合并为一次请求（超过 200 截断）；
 * - 请求完成写入模块级缓存并触发本组件状态更新；
 * - 请求失败静默降级为无框（不写缓存，下次挂载可重试）。
 */
export function useAvatarFrames(
  userIds: string[]
): Record<string, EquippedSlotView> {
  const [frames, setFrames] = useState<Record<string, EquippedSlotView>>({})
  // 去重 + 排序后拼接为稳定 key，避免数组引用变化导致重复请求
  const key = Array.from(new Set(userIds.filter(Boolean))).sort().join(",")

  useEffect(() => {
    let cancelled = false
    const ids = key ? key.split(",") : []
    if (ids.length === 0) {
      setFrames({})
      return
    }

    // 从缓存收集当前可用的头像框
    const collect = () => {
      const result: Record<string, EquippedSlotView> = {}
      for (const id of ids) {
        const frame = frameCache.get(id)
        if (frame) result[id] = frame
      }
      return result
    }

    // 既不在缓存也不在途的 id 合并为一次批量请求（≤200 截断）
    const missing = ids.filter((id) => !frameCache.has(id) && !inflight.has(id))
    if (missing.length > 0) {
      const batch = missing.slice(0, 200)
      const promise = getAvatarFrames(batch)
        .then((map) => {
          // 未出现在响应中的用户记为 null（未装备），避免翻页反复查询
          for (const id of batch) frameCache.set(id, map[id] ?? null)
        })
        .catch(() => {
          // 失败静默：不写缓存，展示无框头像
        })
        .finally(() => {
          for (const id of batch) inflight.delete(id)
        })
      for (const id of batch) inflight.set(id, promise)
    }

    setFrames(collect())

    // 等待本组件关心的在途请求（含其他组件发起的）完成后刷新一次
    const pending = new Set<Promise<void>>()
    for (const id of ids) {
      const promise = inflight.get(id)
      if (promise) pending.add(promise)
    }
    if (pending.size > 0) {
      void Promise.all(pending).then(() => {
        if (!cancelled) setFrames(collect())
      })
    }

    return () => {
      cancelled = true
    }
  }, [key])

  return frames
}
