import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { Link, useSearchParams } from "react-router"
import { PlusIcon, ServerIcon } from "lucide-react"

import { PageHeader } from "~/components/page-header"
import { DeployHistory } from "~/components/sfu-deploy/deploy-history"
import { DeployProgress } from "~/components/sfu-deploy/deploy-progress"
import { DeployServers } from "~/components/sfu-deploy/deploy-servers"
import { DeployWizard } from "~/components/sfu-deploy/deploy-wizard"
import { PreflightChip, PreflightPanel, PreflightSheet } from "~/components/sfu-deploy/preflight-panel"
import { paramsToForm, type DeployFormValues } from "~/components/sfu-deploy/shared"
import { Button, buttonVariants } from "~/components/ui/button"
import { Card, CardContent } from "~/components/ui/card"
import { useAsyncData } from "~/hooks/use-async-data"
import { useSfuDeployment } from "~/hooks/use-sfu-deployment"
import {
  getSfuDeployPreflight,
  listSfuDeployments,
  listSfuDeployServers,
  type SfuDeployment,
  type SfuDeployServer,
} from "~/lib/api"
import { gsap, MOTION, MOTION_OK, useGSAP } from "~/lib/gsap"
import { cn } from "~/lib/utils"

const HISTORY_PAGE = 20

export default function VoiceDeployPage() {
  const [searchParams, setSearchParams] = useSearchParams()
  const activeID = searchParams.get("d")

  const [historyLimit, setHistoryLimit] = useState(HISTORY_PAGE)
  const [preflightOpen, setPreflightOpen] = useState(false)
  /** 表单预填值（沿用配置重试 / 再部署一台 / 从已存服务器发起）。 */
  const [prefill, setPrefill] = useState<{ values?: DeployFormValues; serverID?: string }>({})

  const preflight = useAsyncData(() => getSfuDeployPreflight(), [])
  const history = useAsyncData(() => listSfuDeployments(historyLimit), [historyLimit])
  const servers = useAsyncData(() => listSfuDeployServers(), [])

  const stageRef = useRef<HTMLDivElement>(null)
  const headingRef = useRef<HTMLHeadingElement>(null)
  /** 页面首次挂载时若已有运行中部署，自动进入 live；只做一次。 */
  const autoResumedRef = useRef(false)

  const deployment = useSfuDeployment(activeID, {
    onTerminal: () => {
      history.reload(true)
      servers.reload(true)
    },
  })

  const mode: "form" | "live" | "replay" = !activeID
    ? "form"
    : deployment.running
      ? "live"
      : "replay"

  // 有进行中的部署但 URL 没带参数时自动跟进（刷新页面后仍能续看）。
  useEffect(() => {
    if (autoResumedRef.current || activeID || history.status !== "success") return
    const running = (history.data ?? []).find(d => d.status === "RUNNING")
    if (running) {
      autoResumedRef.current = true
      setSearchParams({ d: running.id }, { replace: true })
    }
  }, [activeID, history.status, history.data, setSearchParams])

  const openDeployment = useCallback(
    (item: SfuDeployment) => setSearchParams({ d: item.id }),
    [setSearchParams]
  )

  const backToForm = useCallback(
    (next?: { values?: DeployFormValues; serverID?: string }) => {
      setPrefill(next ?? {})
      setSearchParams({})
    },
    [setSearchParams]
  )

  // 舞台换形态时做一次入场，并把焦点移到标题（否则键盘用户焦点掉回 body）。
  useGSAP(
    () => {
      const media = gsap.matchMedia()
      media.add(MOTION_OK, () => {
        gsap.fromTo(
          stageRef.current,
          { autoAlpha: 0, y: 16, filter: "blur(var(--panel-blur))" },
          {
            autoAlpha: 1,
            y: 0,
            filter: "blur(0px)",
            duration: MOTION.enter,
            ease: MOTION.ease,
            clearProps: "all",
          }
        )
      })
    },
    { dependencies: [mode, activeID], scope: stageRef }
  )

  useEffect(() => {
    headingRef.current?.focus({ preventScroll: true })
  }, [mode])

  const historyItems = history.data ?? []
  const hasMore = historyItems.length >= historyLimit

  const blockedReason = useMemo(() => {
    if (preflight.status !== "success" || !preflight.data || preflight.data.ok) return undefined
    const failed = preflight.data.checks.filter(c => c.status === "error")
    return `环境预检未通过：${failed.map(c => c.label).join("、")}。修正后才能发起部署。`
  }, [preflight.status, preflight.data])

  return (
    <main className="flex flex-1 flex-col gap-6 py-4 md:py-6">
      <PageHeader
        title="SFU 自动部署"
        titleExtra={
          mode !== "form" ? (
            <PreflightChip data={preflight.data} onClick={() => setPreflightOpen(true)} />
          ) : undefined
        }
        description="通过 SSH 在目标 Linux 服务器上安装 owl-sfu 并自动接入本 Server。"
        actions={
          <div className="flex flex-wrap items-center gap-2">
            {mode !== "form" && (
              <Button onClick={() => backToForm()}>
                <PlusIcon data-icon="inline-start" />
                新建部署
              </Button>
            )}
            {/* 导航用链接：直接给 Link 上按钮样式，保留 <a> 的链接语义
                （包成 Base UI Button 会破坏语义并触发 nativeButton 警告）。 */}
            <Link to="/voice/nodes" className={cn(buttonVariants({ variant: "outline" }))}>
              <ServerIcon data-icon="inline-start" />
              SFU 节点
            </Link>
          </div>
        }
      />

      {mode === "form" && (
        <section className="px-4 lg:px-6" aria-label="环境预检">
          <PreflightPanel
            data={preflight.data}
            status={preflight.status}
            error={preflight.error}
            onRetry={() => preflight.reload()}
          />
        </section>
      )}

      <section className="px-4 lg:px-6" aria-labelledby="deploy-stage-heading">
        <Card>
          <CardContent>
            <div ref={stageRef} className="min-w-0">
              <h2
                ref={headingRef}
                id="deploy-stage-heading"
                tabIndex={-1}
                className="sr-only outline-none"
              >
                {mode === "form" ? "新建部署" : mode === "live" ? "部署进行中" : "部署详情"}
              </h2>

              {mode === "form" ? (
                <DeployWizard
                  initialValues={prefill.values}
                  initialServerID={prefill.serverID}
                  blockedReason={blockedReason}
                  onStarted={id => {
                    history.reload(true)
                    servers.reload(true)
                    setSearchParams({ d: id })
                  }}
                />
              ) : deployment.deployment ? (
                <DeployProgress
                  deployment={deployment.deployment}
                  lines={deployment.lines}
                  running={deployment.running}
                  onCancel={deployment.cancel}
                  onRetry={() =>
                    backToForm({
                      values: paramsToForm(deployment.deployment?.params),
                      serverID: deployment.deployment?.server_id,
                    })
                  }
                  onEditAndRetry={() =>
                    backToForm({
                      values: paramsToForm(deployment.deployment?.params),
                      serverID: deployment.deployment?.server_id,
                    })
                  }
                  onCloneConfig={() => {
                    const values = paramsToForm(deployment.deployment?.params)
                    // 换一台机器：名称与域名必须重填，其余沿用
                    backToForm({
                      values: { ...values, displayName: "", domain: "" },
                      serverID: undefined,
                    })
                  }}
                />
              ) : (
                <p className="py-8 text-center text-sm text-muted-foreground">
                  {deployment.error || "正在读取部署详情…"}
                </p>
              )}
            </div>
          </CardContent>
        </Card>
      </section>

      <section className="grid gap-6 px-4 lg:px-6 xl:grid-cols-[minmax(0,1fr)_380px]" aria-label="部署记录与服务器">
        <div className="min-w-0">
          <h2 className="mb-3 text-sm font-medium">部署记录</h2>
          <DeployHistory
            items={historyItems}
            status={history.status}
            error={history.error}
            selectedID={activeID}
            onSelect={openDeployment}
            onRetryLoad={() => history.reload()}
            onLoadMore={() => setHistoryLimit(limit => limit + HISTORY_PAGE)}
            hasMore={hasMore}
          />
        </div>

        <DeployServers
          servers={servers.data ?? []}
          status={servers.status}
          onChanged={() => servers.reload(true)}
          onDeployTo={(server: SfuDeployServer) => backToForm({ serverID: server.id })}
        />
      </section>

      <PreflightSheet open={preflightOpen} onOpenChange={setPreflightOpen} data={preflight.data} />
    </main>
  )
}
