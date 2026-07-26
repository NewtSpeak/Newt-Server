import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { toast } from "sonner"

import { parseLog, TERMINAL_STATUSES, type LogLine } from "~/components/sfu-deploy/shared"
import { useGatewayEvent } from "~/hooks/use-gateway"
import {
  cancelSfuDeployment,
  getSfuDeployment,
  listSfuDeployments,
  type SfuDeployment,
  type SfuDeploymentEvent,
} from "~/lib/api"

/** Gateway 断线时的兜底轮询间隔。 */
const FALLBACK_POLL_MS = 5_000

export type UseSfuDeploymentResult = {
  deployment: SfuDeployment | null
  log: string
  lines: LogLine[]
  running: boolean
  loading: boolean
  error: string
  cancel: () => Promise<void>
  reload: () => void
}

/**
 * 订阅单次部署的进度与日志。
 *
 * 增量协议：SFU_DEPLOYMENT_UPDATE 事件只携带 log_offset 不带正文，
 * offset 超过本地已知值时回拉一次全量日志。状态字段（status/step）
 * 直接从事件就地合并，让步骤条无需等待网络往返。
 */
export function useSfuDeployment(
  deploymentID: string | null,
  opts?: { onTerminal?: (deployment: SfuDeployment) => void }
): UseSfuDeploymentResult {
  const [deployment, setDeployment] = useState<SfuDeployment | null>(null)
  const [log, setLog] = useState("")
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState("")

  const idRef = useRef<string | null>(null)
  idRef.current = deploymentID
  const logOffsetRef = useRef(0)
  // 回调用 ref 存最新值，避免调用方每次渲染传新函数导致订阅抖动（同 use-gateway 的惯例）。
  const onTerminalRef = useRef(opts?.onTerminal)
  onTerminalRef.current = opts?.onTerminal

  const fetchDeployment = useCallback(async (id: string, silent = true) => {
    if (!silent) setLoading(true)
    try {
      const data = await getSfuDeployment(id)
      // 请求期间用户可能已切走，丢弃过期响应。
      if (idRef.current !== id) return null
      setDeployment(data.deployment)
      setLog(data.log)
      logOffsetRef.current = data.log_offset
      setError("")
      return data.deployment
    } catch (reason) {
      if (idRef.current === id) setError(reason instanceof Error ? reason.message : "读取部署详情失败")
      return null
    } finally {
      if (!silent) setLoading(false)
    }
  }, [])

  // 切换部署：重置本地状态后拉取全量。
  useEffect(() => {
    if (!deploymentID) {
      setDeployment(null)
      setLog("")
      setError("")
      logOffsetRef.current = 0
      return
    }
    setDeployment(null)
    setLog("")
    logOffsetRef.current = 0
    void fetchDeployment(deploymentID, false)
  }, [deploymentID, fetchDeployment])

  useGatewayEvent("SFU_DEPLOYMENT_UPDATE", payload => {
    const event = payload as SfuDeploymentEvent
    const current = idRef.current
    if (!current || event.deployment_id !== current) return

    setDeployment(prev =>
      prev
        ? {
            ...prev,
            status: event.status,
            step: event.step,
            error: event.error ?? prev.error,
            node_id: event.node_id ?? prev.node_id,
          }
        : prev
    )

    if (event.log_offset > logOffsetRef.current) {
      logOffsetRef.current = event.log_offset
      void fetchDeployment(event.deployment_id)
    }

    if (TERMINAL_STATUSES.has(event.status)) {
      // 终态再拉一次，确保拿到完整日志与后端最终写入的错误信息。
      void fetchDeployment(event.deployment_id).then(final => {
        if (final) onTerminalRef.current?.(final)
      })
    }
  })

  const running = deployment != null && !TERMINAL_STATUSES.has(deployment.status)

  // Gateway 断线兜底：运行中每 5s 拉一次。
  useEffect(() => {
    if (!running || !deploymentID) return
    const timer = setInterval(() => void fetchDeployment(deploymentID), FALLBACK_POLL_MS)
    return () => clearInterval(timer)
  }, [running, deploymentID, fetchDeployment])

  const lines = useMemo(() => parseLog(log), [log])

  const cancel = useCallback(async () => {
    const id = idRef.current
    if (!id) return
    try {
      await cancelSfuDeployment(id)
      toast.info("已请求取消部署")
      void fetchDeployment(id)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "取消失败")
    }
  }, [fetchDeployment])

  const reload = useCallback(() => {
    const id = idRef.current
    if (id) void fetchDeployment(id)
  }, [fetchDeployment])

  return { deployment, log, lines, running, loading, error, cancel, reload }
}

/**
 * 查询当前是否有进行中的部署，供 SFU 节点页横幅与控制台自动进入 live 模式使用。
 */
export function useRunningSfuDeployment(pollMs = 15_000) {
  const [running, setRunning] = useState<SfuDeployment | null>(null)

  const refresh = useCallback(() => {
    void listSfuDeployments(10)
      .then(list => setRunning(list.find(d => d.status === "RUNNING") ?? null))
      .catch(() => setRunning(null))
  }, [])

  useEffect(() => {
    refresh()
    const timer = setInterval(refresh, pollMs)
    return () => clearInterval(timer)
  }, [refresh, pollMs])

  useGatewayEvent("SFU_DEPLOYMENT_UPDATE", () => refresh())

  return { running, refresh }
}
