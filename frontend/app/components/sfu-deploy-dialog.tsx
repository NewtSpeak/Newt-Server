import { useEffect, useState } from "react"
import { Link } from "react-router"
import { ExternalLinkIcon } from "lucide-react"

import { DeployProgress } from "~/components/sfu-deploy/deploy-progress"
import { DeployWizard } from "~/components/sfu-deploy/deploy-wizard"
import { Button, buttonVariants } from "~/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "~/components/ui/dialog"
import { useSfuDeployment } from "~/hooks/use-sfu-deployment"
import { cn } from "~/lib/utils"

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 恢复查看某次进行中的部署（打开时直接进入进度视图） */
  resumeDeploymentID?: string | null
  /** 部署产生节点变化时通知外层刷新列表 */
  onNodesChanged: () => void
}

/**
 * SFU 节点页的部署快捷入口。
 *
 * 完整能力（环境预检、服务器资产、历史回看、日志搜索导出）在 /voice/deploy
 * 控制台页；这里只是同一套组件的紧凑变体，随时可一键升格到控制台。
 */
export function SfuDeployDialog({ open, onOpenChange, resumeDeploymentID, onNodesChanged }: Props) {
  const [activeID, setActiveID] = useState<string | null>(null)

  useEffect(() => {
    if (open) setActiveID(resumeDeploymentID ?? null)
  }, [open, resumeDeploymentID])

  const deployment = useSfuDeployment(activeID, { onTerminal: onNodesChanged })

  return (
    <Dialog
      open={open}
      onOpenChange={next => {
        onOpenChange(next)
        if (!next) setActiveID(null)
      }}
    >
      <DialogContent className="max-w-3xl">
        {activeID ? (
          <>
            <DialogHeader>
              <DialogTitle>部署 SFU 节点</DialogTitle>
              <DialogDescription>可以关闭此窗口，部署会在后台继续。</DialogDescription>
            </DialogHeader>
            {deployment.deployment ? (
              <DeployProgress
                compact
                deployment={deployment.deployment}
                lines={deployment.lines}
                running={deployment.running}
                onCancel={deployment.cancel}
              />
            ) : (
              <p className="py-8 text-center text-sm text-muted-foreground">
                {deployment.error || "正在读取部署详情…"}
              </p>
            )}
          </>
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>自动部署到服务器</DialogTitle>
              <DialogDescription>
                通过 SSH 在目标 Linux 服务器上自动安装 owl-sfu，并让它自动接入本 Server。
              </DialogDescription>
            </DialogHeader>
            <DeployWizard
              variant="dialog"
              onStarted={id => {
                setActiveID(id)
                onNodesChanged()
              }}
            />
          </>
        )}

        <DialogFooter className="sm:justify-between">
          <Link
            to={activeID ? `/voice/deploy?d=${activeID}` : "/voice/deploy"}
            className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}
          >
            <ExternalLinkIcon data-icon="inline-start" />
            在部署控制台中打开
          </Link>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {deployment.running ? "后台运行" : "关闭"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
