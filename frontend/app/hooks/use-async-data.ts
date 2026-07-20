import { useCallback, useEffect, useRef, useState } from "react"

type AsyncStatus = "idle" | "loading" | "success" | "error"

type AsyncState<T> = {
  status: AsyncStatus
  data: T | null
  error: string
}

/**
 * 统一 loading / empty / error 三态的数据加载 hook。
 * fetcher 为 null 表示前置条件未满足（如尚未选择服务器），保持 idle。
 * reload(true) 为静默刷新：不回退到 loading，适合轮询与 Gateway 事件驱动的局部刷新。
 */
export function useAsyncData<T>(fetcher: (() => Promise<T>) | null, deps: unknown[]) {
  const [state, setState] = useState<AsyncState<T>>({ status: fetcher ? "loading" : "idle", data: null, error: "" })
  const fetcherRef = useRef(fetcher)
  fetcherRef.current = fetcher
  const versionRef = useRef(0)

  const reload = useCallback((silent = false) => {
    const fn = fetcherRef.current
    const version = ++versionRef.current
    if (!fn) {
      setState({ status: "idle", data: null, error: "" })
      return
    }
    if (!silent) setState(current => ({ ...current, status: "loading", error: "" }))
    fn()
      .then(data => {
        if (versionRef.current === version) setState({ status: "success", data, error: "" })
      })
      .catch(reason => {
        if (versionRef.current === version)
          setState(current => ({
            status: "error",
            data: current.data,
            error: reason instanceof Error ? reason.message : "请求失败",
          }))
      })
  }, [])

  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => reload(), deps)

  return { ...state, reload }
}
