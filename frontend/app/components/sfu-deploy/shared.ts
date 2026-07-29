/**
 * SFU 自动部署控制台的无 UI 共享层：常量、日志解析、风险与诊断规则、
 * 部署参数与表单之间的双向映射。全部为纯函数，可单测。
 */
import type { SfuDeployment, SfuDeploymentStep, SfuDeployTLSMode } from "~/lib/api"

// ---- 步骤 ----

export type StepDef = { key: SfuDeploymentStep; label: string; hint: string }

/** 与后端 model.SfuDeployStep* 常量一一对应，顺序即执行顺序。 */
export const STEPS: StepDef[] = [
  { key: "CONNECTING", label: "连接服务器", hint: "建立 SSH 连接并校验主机指纹" },
  { key: "PRECHECK", label: "环境检查", hint: "确认系统架构、权限与到本 Server 的连通性" },
  { key: "INSTALL_DEPS", label: "安装依赖与程序", hint: "安装系统依赖并下载 newt-sfu 二进制" },
  { key: "CREATE_NODE", label: "签发接入凭证", hint: "创建节点占位并签发一次性 enrollment token" },
  { key: "CONFIGURE", label: "写入配置并启动", hint: "写 env 与 systemd 单元，启动服务" },
  { key: "WAIT_ONLINE", label: "等待节点上线", hint: "等待节点自行接入并建立控制通道" },
  { key: "ENABLE_SCHEDULING", label: "开启调度", hint: "允许该节点承载用户流量" },
  { key: "DONE", label: "完成", hint: "" },
]

export const TERMINAL_STATUSES = new Set(["SUCCEEDED", "FAILED", "CANCELED"])

export type StepState = "pending" | "active" | "done" | "failed" | "skipped"

/** 逐步推导每一步的视觉状态。skipped 用于未开启调度时被跳过的 ENABLE_SCHEDULING。 */
export function deriveStepStates(deployment: SfuDeployment | null): StepState[] {
  if (!deployment) return STEPS.map(() => "pending")
  const currentIndex = STEPS.findIndex(s => s.key === deployment.step)
  const skipScheduling = deployment.params?.enable_scheduling === false
  const succeeded = deployment.status === "SUCCEEDED"
  const failed = deployment.status === "FAILED" || deployment.status === "CANCELED"

  return STEPS.map((step, index) => {
    const isSkippable = step.key === "ENABLE_SCHEDULING" && skipScheduling
    if (succeeded) return isSkippable ? "skipped" : "done"
    if (currentIndex < 0) return "pending"
    if (index < currentIndex) return isSkippable ? "skipped" : "done"
    if (index > currentIndex) return "pending"
    return failed ? "failed" : "active"
  })
}

/** 当前所处步骤的序号（用于进度条与「第 N / 8 步」）。 */
export function currentStepIndex(deployment: SfuDeployment | null): number {
  if (!deployment) return -1
  return STEPS.findIndex(s => s.key === deployment.step)
}

// ---- 日志 ----

export type LogLevel = "info" | "warn" | "error" | "notice" | "meta"

export type LogLine = {
  /** 1 起的行号；日志无时间戳，行号是唯一定位坐标。 */
  n: number
  level: LogLevel
  /** 远端脚本前缀（[+] / [!] / [x]），保留可见以免语义只靠颜色传递。 */
  prefix: string
  body: string
}

const TRUNCATION_MARK = "…（较早日志已截断）"

/**
 * 解析部署日志。两类来源：
 *   - 远端 bash 脚本：行首 [+] / [!] / [x]（info / warn / error）
 *   - Go 编排器里程碑消息：无前缀，单列为 notice 级避免埋没在远端输出里
 */
export function parseLog(log: string): LogLine[] {
  if (!log) return []
  const raw = log.split("\n")
  // 末尾换行产生的空串不算一行
  if (raw.length > 0 && raw[raw.length - 1] === "") raw.pop()
  return raw.map((line, index) => {
    const n = index + 1
    if (line.startsWith(TRUNCATION_MARK)) return { n, level: "meta" as const, prefix: "", body: line }
    if (line.startsWith("[+] ")) return { n, level: "info" as const, prefix: "[+]", body: line.slice(4) }
    if (line.startsWith("[!] ")) return { n, level: "warn" as const, prefix: "[!]", body: line.slice(4) }
    if (line.startsWith("[x] ")) return { n, level: "error" as const, prefix: "[x]", body: line.slice(4) }
    return { n, level: "notice" as const, prefix: "", body: line }
  })
}

/** 把日志行还原成可复制/下载的纯文本（保留原始前缀）。 */
export function linesToText(lines: LogLine[]): string {
  return lines.map(l => (l.prefix ? `${l.prefix} ${l.body}` : l.body)).join("\n")
}

// ---- 接入地址推导 ----

/**
 * 由部署参数推导客户端接入地址，逻辑对齐后端 sfudeploy.normalizedSpec.endpoints()。
 * 不解析日志文本，因此在部署尚未产出日志时也能实时预览。
 */
export function deriveAdvertiseURL(params: {
  tls_mode?: SfuDeployTLSMode | string
  domain?: string
  public_ip?: string
  host?: string
}): string {
  const domain = (params.domain ?? "").trim()
  const ip = (params.public_ip ?? "").trim() || (params.host ?? "").trim()
  switch (params.tls_mode) {
    case "caddy":
      return domain ? `wss://${domain}/ws` : ""
    case "custom":
      return domain ? `wss://${domain}:8443/ws` : ""
    default:
      return ip ? `ws://${ip}:8443/ws` : ""
  }
}

// ---- TLS 方案 ----

export const TLS_OPTIONS: {
  value: SfuDeployTLSMode
  title: string
  desc: string
  tone?: "recommended" | "danger"
}[] = [
  {
    value: "caddy",
    title: "自动申请证书",
    desc: "在目标机安装 Caddy 并占用 80/443，自动签发并续期证书。客户端连 wss://{域名}/ws",
    tone: "recommended",
  },
  {
    value: "custom",
    title: "使用已有证书",
    desc: "提供服务器上的证书与私钥路径，SFU 直接监听 8443。客户端连 wss://{域名}:8443/ws",
  },
  {
    value: "none",
    title: "不启用 TLS",
    desc: "明文 ws://{公网IP}:8443/ws。浏览器在 HTTPS 页面下会拒绝连接，仅限内网与测试",
    tone: "danger",
  },
]

// ---- 风险清单 ----

export type RiskTone = "danger" | "warn" | "info"
export type Risk = { key: string; tone: RiskTone; text: string }

export type RiskInput = {
  tlsMode: SfuDeployTLSMode
  domain: string
  host: string
  mediaUdpPort: number
  forceReinstall: boolean
  trustNewHostKey: boolean
  configureUFW: boolean
  enableScheduling: boolean
  enableCascade: boolean
  saveAs?: string
}

/** 汇总本次部署会对目标机产生的影响，按严重度排序。 */
export function collectRisks(input: RiskInput): Risk[] {
  const risks: Risk[] = []
  if (input.forceReinstall) {
    risks.push({
      key: "force_reinstall",
      tone: "danger",
      text: "覆盖目标机上已有的 newt-sfu 安装与配置，原节点将被弃用且需在节点列表手动吊销",
    })
  }
  if (input.tlsMode === "none") {
    risks.push({
      key: "tls_none",
      tone: "danger",
      text: "信令为明文 ws://，链路可被窃听；浏览器在 HTTPS 页面下会直接拒绝连接",
    })
  }
  if (input.trustNewHostKey) {
    risks.push({
      key: "trust_hostkey",
      tone: "warn",
      text: "将信任目标机的新主机指纹并覆盖已记录值；若非本人重装过该机，可能存在中间人",
    })
  }
  if (input.configureUFW) {
    const ports = ["22/tcp", "80/tcp", "443/tcp", `${input.mediaUdpPort}/udp`]
    if (input.enableCascade) ports.push("8843/tcp")
    risks.push({
      key: "ufw",
      tone: "warn",
      text: `修改目标机防火墙规则，放行 ${ports.join("、")}`,
    })
  }
  if (input.enableScheduling) {
    risks.push({
      key: "scheduling",
      tone: "warn",
      text: "节点上线后立即进入调度池，将开始承载真实用户流量",
    })
  }
  if (input.tlsMode === "caddy") {
    risks.push({
      key: "caddy",
      tone: "info",
      text: `在目标机安装 Caddy 并占用 80/443；需 ${input.domain || "该域名"} 的 A 记录已指向 ${input.host || "目标机"}`,
    })
  }
  if (input.saveAs?.trim()) {
    risks.push({
      key: "save",
      tone: "info",
      text: `SSH 凭据将加密保存为「${input.saveAs.trim()}」，后续可直接复用`,
    })
  }
  return risks
}

export function hasBlockingRisk(risks: Risk[]): boolean {
  return risks.some(r => r.tone === "danger")
}

// ---- 失败诊断 ----

export type Diagnosis = { cause: string; actions: string[] }

type DiagnosisRule = {
  /** 限定失败步骤；省略表示任意步骤。 */
  step?: SfuDeploymentStep
  /** 错误文本关键词，命中任意一个即匹配。 */
  keywords: string[]
  cause: string
  actions: string[]
}

/**
 * 把 SSH 部署的黑盒错误翻译成可执行的动作。
 * 顺序即优先级：先匹配到的规则胜出。
 */
export const DIAGNOSIS_RULES: DiagnosisRule[] = [
  {
    step: "CONNECTING",
    keywords: ["指纹", "fingerprint", "中间人"],
    cause: "目标机的 SSH 主机密钥与上次记录的不一致，通常是重装系统或更换了主机密钥。",
    actions: [
      "确认该机器确实由你重装过，排除中间人攻击",
      "在高级选项中勾选「信任新的主机指纹」后重试",
    ],
  },
  {
    step: "CONNECTING",
    keywords: ["认证", "auth", "password", "私钥", "unable to authenticate"],
    cause: "SSH 凭据不被接受：密码错误、私钥不匹配，或该用户被禁止密码登录。",
    actions: [
      "核对用户名与密码；私钥登录需确认公钥已写入目标机 ~/.ssh/authorized_keys",
      "若使用带口令的私钥，确认口令填写正确",
      "检查目标机 sshd_config 是否禁用了 PasswordAuthentication",
    ],
  },
  {
    step: "CONNECTING",
    keywords: ["连接", "dial", "timeout", "refused", "no route"],
    cause: "无法建立到目标机 SSH 端口的 TCP 连接。",
    actions: ["确认 IP 与 SSH 端口正确、目标机已开机", "检查目标机安全组/防火墙是否放行 SSH 端口"],
  },
  {
    step: "PRECHECK",
    keywords: ["sudo", "root"],
    cause: "登录用户不是 root，且无法通过 sudo 提权。",
    actions: [
      "在连接信息中填写 sudo 密码",
      "或在目标机为该用户配置免密 sudo（NOPASSWD）",
      "或改用 root 账号登录",
    ],
  },
  {
    keywords: ["已安装 newt-sfu", "已安装", "newt-sfu.env"],
    cause: "目标机上已经存在一份 newt-sfu 安装。",
    actions: [
      "若要覆盖，在高级选项勾选「强制重装」后重试（原节点需手动吊销）",
      "若这台机器已在正常服务，请换一台目标机",
    ],
  },
  {
    keywords: ["架构", "Linux", "arch", "不支持"],
    cause: "目标机的操作系统或 CPU 架构不受支持。",
    actions: [
      "自动部署仅支持 Linux（amd64 或 arm64）",
      "若为 arm64 机器，确认发布目录中存在对应架构的 newt-sfu 工件",
    ],
  },
  {
    step: "INSTALL_DEPS",
    keywords: ["下载", "curl", "404", "sha256", "校验"],
    cause: "目标机无法下载 newt-sfu 二进制，或下载内容校验失败。",
    actions: [
      "确认目标机能访问本 Server 的 PUBLIC_BASE_URL（可在目标机上手动 curl 验证）",
      "检查发布目录中是否存在对应架构的 newt-sfu 工件",
      "若走了代理或 CDN，确认未缓存到损坏的文件",
    ],
  },
  {
    step: "CONFIGURE",
    keywords: ["443", "80", "caddy", "占用", "address already in use"],
    cause: "Caddy 需要的 80/443 端口已被其他服务（常见为 nginx、apache）占用。",
    actions: [
      "停用占用这两个端口的服务后重试",
      "或改用「使用已有证书」方案，由 SFU 直接监听 8443",
    ],
  },
  {
    step: "CONFIGURE",
    keywords: ["systemd", "启动失败", "healthz"],
    cause: "newt-sfu 服务已安装但未能正常启动。",
    actions: [
      "查看下方日志中的 journalctl 输出定位崩溃原因",
      "确认媒体 UDP 端口未被其他进程占用",
    ],
  },
  {
    step: "WAIT_ONLINE",
    keywords: [],
    cause: "节点已在目标机启动，但没能在超时内回连到本 Server 的控制面。",
    actions: [
      "确认 SFU_CONTROL_PUBLIC_ENDPOINT 是远程可达的公网地址，而不是回环地址",
      "检查目标机的出站防火墙是否放行到该地址的连接",
      "在目标机执行 journalctl -u newt-sfu -n 100 查看 enroll 失败原因",
    ],
  },
]

/** 按失败步骤与错误文本匹配诊断建议；无匹配返回 null。 */
export function diagnose(deployment: SfuDeployment | null): Diagnosis | null {
  if (!deployment || deployment.status !== "FAILED") return null
  const error = (deployment.error ?? "").toLowerCase()
  for (const rule of DIAGNOSIS_RULES) {
    if (rule.step && rule.step !== deployment.step) continue
    if (rule.keywords.length > 0 && !rule.keywords.some(k => error.includes(k.toLowerCase()))) continue
    return { cause: rule.cause, actions: rule.actions }
  }
  return null
}

// ---- 参数 ↔ 表单 ----

/** 部署向导的表单模型（节点与选项部分；连接信息单独管理）。 */
export type DeployFormValues = {
  displayName: string
  region: string
  tlsMode: SfuDeployTLSMode
  domain: string
  certFile: string
  keyFile: string
  publicIP: string
  mediaUdpPort: string
  maxUsers: string
  release: string
  enableCascade: boolean
  enableScheduling: boolean
  configureUFW: boolean
  forceReinstall: boolean
  trustNewHostKey: boolean
}

export const DEFAULT_FORM: DeployFormValues = {
  displayName: "",
  region: "",
  tlsMode: "caddy",
  domain: "",
  certFile: "",
  keyFile: "",
  publicIP: "",
  mediaUdpPort: "3478",
  maxUsers: "1200",
  release: "",
  enableCascade: false,
  enableScheduling: true,
  configureUFW: true,
  forceReinstall: false,
  trustNewHostKey: false,
}

/** 高级选项中被改成非默认值的字段数，折叠时以徽章提示，避免用户忘记自己改过。 */
export function countAdvancedChanges(values: DeployFormValues): number {
  const advanced: (keyof DeployFormValues)[] = [
    "region", "publicIP", "mediaUdpPort", "maxUsers", "release",
    "enableCascade", "enableScheduling", "configureUFW", "forceReinstall", "trustNewHostKey",
  ]
  return advanced.filter(key => values[key] !== DEFAULT_FORM[key]).length
}

/**
 * 把历史部署的 params 快照还原成表单值，用于「沿用配置重试」与「再部署一台」。
 * 后端 paramsSnapshot 存了全部字段，因此无需额外接口。
 */
export function paramsToForm(params: Record<string, unknown> | undefined): DeployFormValues {
  if (!params) return { ...DEFAULT_FORM }
  const str = (v: unknown, fallback = "") => (typeof v === "string" ? v : fallback)
  const num = (v: unknown, fallback: string) =>
    typeof v === "number" && Number.isFinite(v) ? String(v) : fallback
  const bool = (v: unknown, fallback: boolean) => (typeof v === "boolean" ? v : fallback)
  const labels = (params.labels ?? {}) as Record<string, string> | null

  return {
    displayName: str(params.display_name),
    region: str(labels?.region),
    tlsMode: (["caddy", "custom", "none"].includes(str(params.tls_mode))
      ? str(params.tls_mode)
      : "caddy") as SfuDeployTLSMode,
    domain: str(params.domain),
    certFile: str(params.tls_cert_file),
    keyFile: str(params.tls_key_file),
    publicIP: str(params.public_ip),
    mediaUdpPort: num(params.media_udp_port, DEFAULT_FORM.mediaUdpPort),
    maxUsers: num(params.max_users, DEFAULT_FORM.maxUsers),
    release: str(params.release),
    enableCascade: bool(params.enable_cascade, false),
    enableScheduling: bool(params.enable_scheduling, true),
    configureUFW: bool(params.configure_ufw, true),
    forceReinstall: bool(params.force_reinstall, false),
    trustNewHostKey: false, // 指纹信任不继承，每次都要显式确认
  }
}

/** 表单值 → createSfuDeployment 的 node / options 载荷。 */
export function formToRequest(values: DeployFormValues) {
  const region = values.region.trim()
  return {
    node: {
      display_name: values.displayName.trim(),
      labels: region ? { region } : undefined,
      tls_mode: values.tlsMode,
      domain: values.domain.trim() || undefined,
      tls_cert_file: values.tlsMode === "custom" ? values.certFile.trim() : undefined,
      tls_key_file: values.tlsMode === "custom" ? values.keyFile.trim() : undefined,
      public_ip: values.publicIP.trim() || undefined,
      media_udp_port: Number(values.mediaUdpPort) || 3478,
      max_users: Number(values.maxUsers) || 1200,
      enable_cascade: values.enableCascade,
      release: values.release || undefined,
      enable_scheduling: values.enableScheduling,
    },
    options: {
      configure_ufw: values.configureUFW,
      force_reinstall: values.forceReinstall,
      trust_new_hostkey: values.trustNewHostKey,
    },
  }
}

// ---- 校验 ----

export type FieldErrors = Partial<Record<string, string>>

/** 表单校验：返回字段名 → 错误文案。空对象表示通过。 */
export function validateForm(
  values: DeployFormValues,
  connection: { useSaved: boolean; host: string; password: string; privateKey: string; authMethod: string }
): FieldErrors {
  const errors: FieldErrors = {}
  if (!values.displayName.trim()) errors.displayName = "请填写节点名称"
  else if (values.displayName.trim().length > 100) errors.displayName = "节点名称不超过 100 个字符"

  if (!connection.useSaved) {
    if (!connection.host.trim()) errors.host = "请填写服务器 IP 或域名"
    if (connection.authMethod === "password" && !connection.password) errors.password = "请填写 SSH 密码"
    if (connection.authMethod === "private_key" && !connection.privateKey.trim()) {
      errors.privateKey = "请粘贴 SSH 私钥"
    }
  }

  if (values.tlsMode !== "none" && !values.domain.trim()) {
    errors.domain = "该 TLS 方案需要填写域名"
  }
  if (values.tlsMode === "custom") {
    if (!values.certFile.trim()) errors.certFile = "请填写证书路径"
    if (!values.keyFile.trim()) errors.keyFile = "请填写私钥路径"
  }

  const port = Number(values.mediaUdpPort)
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    errors.mediaUdpPort = "端口需在 1–65535 之间"
  }
  const users = Number(values.maxUsers)
  if (!Number.isInteger(users) || users < 1) errors.maxUsers = "最大用户数需为正整数"

  return errors
}
