import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { ChevronDownIcon, PlusIcon, ServerIcon } from "lucide-react"
import { toast } from "sonner"

import { DeployConfirmDialog } from "~/components/sfu-deploy/deploy-confirm-dialog"
import { DeploySummaryRail } from "~/components/sfu-deploy/deploy-summary-rail"
import {
  collectRisks,
  countAdvancedChanges,
  DEFAULT_FORM,
  formToRequest,
  TLS_OPTIONS,
  validateForm,
  type DeployFormValues,
  type FieldErrors,
} from "~/components/sfu-deploy/shared"
import { Badge } from "~/components/ui/badge"
import { Button } from "~/components/ui/button"
import { Checkbox } from "~/components/ui/checkbox"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "~/components/ui/collapsible"
import {
  Field,
  FieldContent,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
  FieldLegend,
  FieldSeparator,
  FieldSet,
  FieldTitle,
} from "~/components/ui/field"
import { Input } from "~/components/ui/input"
import { RadioGroup, RadioGroupItem } from "~/components/ui/radio-group"
import { SimpleSelect } from "~/components/simple-select"
import { Textarea } from "~/components/ui/textarea"
import { ToggleGroup, ToggleGroupItem } from "~/components/ui/toggle-group"
import {
  createSfuDeployment,
  listSfuDeployServers,
  listSfuReleases,
  type SfuDeployConnection,
  type SfuDeployServer,
  type SfuDeployTLSMode,
  type SfuRelease,
} from "~/lib/api"
import { cn } from "~/lib/utils"

type ConnectionMode = "saved" | "new"

type Props = {
  /** 预填的表单值（沿用配置重试 / 再部署一台）。 */
  initialValues?: DeployFormValues
  /** 预选的已存服务器。 */
  initialServerID?: string
  onStarted: (deploymentID: string) => void
  /** 环境预检未通过时禁用提交并说明原因。 */
  blockedReason?: string
  variant?: "page" | "dialog"
  className?: string
}

export function DeployWizard({
  initialValues,
  initialServerID,
  onStarted,
  blockedReason,
  variant = "page",
  className,
}: Props) {
  const [values, setValues] = useState<DeployFormValues>(initialValues ?? { ...DEFAULT_FORM })
  const [errors, setErrors] = useState<FieldErrors>({})

  // 连接信息（凭据只在内存中，提交后立即清空）
  const [servers, setServers] = useState<SfuDeployServer[]>([])
  const [mode, setMode] = useState<ConnectionMode>(initialServerID ? "saved" : "new")
  const [serverID, setServerID] = useState(initialServerID ?? "")
  const [host, setHost] = useState("")
  const [port, setPort] = useState("22")
  const [username, setUsername] = useState("root")
  const [authMethod, setAuthMethod] = useState<"password" | "private_key">("password")
  const [password, setPassword] = useState("")
  const [privateKey, setPrivateKey] = useState("")
  const [passphrase, setPassphrase] = useState("")
  const [sudoPassword, setSudoPassword] = useState("")
  const [saveServer, setSaveServer] = useState(true)
  const [saveAs, setSaveAs] = useState("")

  const [releases, setReleases] = useState<SfuRelease[]>([])
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [submitting, setSubmitting] = useState(false)

  const formRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    void listSfuDeployServers()
      .then(list => {
        setServers(list)
        if (list.length === 0) setMode("new")
      })
      .catch(() => setServers([]))
    void listSfuReleases()
      .then(data => setReleases((data.releases ?? []).filter(r => r.goos === "linux")))
      .catch(() => setReleases([]))
  }, [])

  useEffect(() => {
    if (initialValues) setValues(initialValues)
  }, [initialValues])

  useEffect(() => {
    if (initialServerID) {
      setServerID(initialServerID)
      setMode("saved")
    }
  }, [initialServerID])

  const selectedServer = servers.find(s => s.id === serverID)
  const useSaved = mode === "saved" && Boolean(serverID)
  const effectiveHost = useSaved ? (selectedServer?.host ?? "") : host
  const effectiveUser = useSaved ? (selectedServer?.username ?? "") : username
  const effectivePort = useSaved ? String(selectedServer?.port ?? 22) : port

  const set = useCallback(<K extends keyof DeployFormValues>(key: K, value: DeployFormValues[K]) => {
    setValues(prev => ({ ...prev, [key]: value }))
    setErrors(prev => (prev[key] ? { ...prev, [key]: undefined } : prev))
  }, [])

  const risks = useMemo(
    () =>
      collectRisks({
        tlsMode: values.tlsMode,
        domain: values.domain,
        host: effectiveHost,
        mediaUdpPort: Number(values.mediaUdpPort) || 3478,
        forceReinstall: values.forceReinstall,
        trustNewHostKey: values.trustNewHostKey,
        configureUFW: values.configureUFW,
        enableScheduling: values.enableScheduling,
        enableCascade: values.enableCascade,
        saveAs: !useSaved && saveServer ? saveAs || effectiveHost : "",
      }),
    [values, effectiveHost, useSaved, saveServer, saveAs]
  )

  const advancedChanges = useMemo(() => countAdvancedChanges(values), [values])

  const focusFirstError = useCallback((nextErrors: FieldErrors) => {
    const firstKey = Object.keys(nextErrors).find(key => nextErrors[key])
    if (!firstKey) return
    const el = formRef.current?.querySelector<HTMLElement>(`[data-field="${firstKey}"]`)
    el?.focus()
    el?.scrollIntoView({ block: "center", behavior: "smooth" })
  }, [])

  const attemptSubmit = useCallback(() => {
    if (blockedReason) {
      toast.error(blockedReason)
      return
    }
    const nextErrors = validateForm(values, {
      useSaved,
      host,
      password,
      privateKey,
      authMethod,
    })
    setErrors(nextErrors)
    if (Object.keys(nextErrors).some(key => nextErrors[key])) {
      // 高级选项里的字段出错时先展开，否则用户看不到错误
      if (nextErrors.mediaUdpPort || nextErrors.maxUsers) setAdvancedOpen(true)
      focusFirstError(nextErrors)
      return
    }
    if (risks.length > 0) {
      setConfirmOpen(true)
      return
    }
    void submit()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [blockedReason, values, useSaved, host, password, privateKey, authMethod, risks, focusFirstError])

  async function submit() {
    setSubmitting(true)
    try {
      const connection: SfuDeployConnection | undefined = useSaved
        ? undefined
        : {
            host: host.trim(),
            port: Number(port) || 22,
            username: username.trim() || "root",
            auth_method: authMethod,
            password: authMethod === "password" ? password : undefined,
            private_key: authMethod === "private_key" ? privateKey : undefined,
            passphrase: passphrase || undefined,
            sudo_password: sudoPassword || undefined,
            save_as: saveServer ? saveAs.trim() || host.trim() : undefined,
          }
      const { deployment_id } = await createSfuDeployment({
        server_id: useSaved ? serverID : undefined,
        connection,
        ...formToRequest(values),
      })
      // 凭据用完即清，不在内存中多留一刻
      setPassword("")
      setPrivateKey("")
      setPassphrase("")
      setSudoPassword("")
      setConfirmOpen(false)
      onStarted(deployment_id)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "发起部署失败")
    } finally {
      setSubmitting(false)
    }
  }

  const isDialog = variant === "dialog"

  const form = (
    <div
      ref={formRef}
      onKeyDown={event => {
        if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
          event.preventDefault()
          attemptSubmit()
        }
      }}
      className={cn("min-w-0", isDialog && "max-h-[58vh] overflow-auto pr-1")}
    >
      <FieldGroup>
        {/* ---- 1 目标服务器 ---- */}
        <FieldSet>
          <FieldLegend variant="label">
            <span className="mr-2 inline-grid size-5 place-items-center rounded-full bg-muted text-[11px] font-medium tabular-nums">
              1
            </span>
            目标服务器
          </FieldLegend>
          <FieldDescription>选择已保存的服务器，或输入新的 SSH 连接信息。</FieldDescription>

          {servers.length > 0 && (
            <Field orientation="vertical" className="mt-3">
              <ToggleGroup
                value={[mode]}
                onValueChange={value => {
                  const next = (value[0] as ConnectionMode) ?? "new"
                  setMode(next)
                  if (next === "new") setServerID("")
                }}
                spacing={0}
                variant="outline"
                className="w-fit"
              >
                <ToggleGroupItem value="saved" variant="outline">
                  <ServerIcon data-icon="inline-start" />
                  已保存的服务器
                  <Badge variant="secondary" className="ml-1.5 tabular-nums">
                    {servers.length}
                  </Badge>
                </ToggleGroupItem>
                <ToggleGroupItem value="new" variant="outline">
                  <PlusIcon data-icon="inline-start" />
                  新服务器
                </ToggleGroupItem>
              </ToggleGroup>
            </Field>
          )}

          {mode === "saved" && servers.length > 0 && (
            <RadioGroup
              value={serverID}
              onValueChange={value => setServerID(String(value))}
              aria-label="选择已保存的服务器"
              className="mt-3 max-h-56 gap-2 overflow-auto"
            >
              {servers.map(server => (
                <FieldLabel
                  key={server.id}
                  htmlFor={`server-${server.id}`}
                  className="transition-[background-color,border-color] duration-200 ease-(--modal-ease) has-data-checked:border-primary/45"
                >
                  <Field orientation="horizontal">
                    <RadioGroupItem value={server.id} id={`server-${server.id}`} />
                    <FieldContent>
                      <FieldTitle>{server.name}</FieldTitle>
                      <FieldDescription className="font-mono text-xs">
                        {server.username}@{server.host}:{server.port}
                        {server.host_key_fingerprint && (
                          <>
                            {" · "}
                            {server.host_key_fingerprint.slice(0, 26)}…
                          </>
                        )}
                      </FieldDescription>
                    </FieldContent>
                  </Field>
                </FieldLabel>
              ))}
            </RadioGroup>
          )}

          {mode === "new" && (
            <div className="mt-3 grid gap-4">
              <div className={cn("grid gap-4", !isDialog && "sm:grid-cols-[1fr_110px]")}>
                <Field>
                  <FieldLabel htmlFor="deploy-host">服务器 IP 或域名</FieldLabel>
                  <Input
                    id="deploy-host"
                    data-field="host"
                    value={host}
                    onChange={event => {
                      setHost(event.target.value)
                      setErrors(prev => (prev.host ? { ...prev, host: undefined } : prev))
                    }}
                    placeholder="如 203.0.113.10"
                    aria-invalid={Boolean(errors.host)}
                    aria-describedby={errors.host ? "err-host" : undefined}
                  />
                  {errors.host && <FieldError id="err-host">{errors.host}</FieldError>}
                </Field>
                <Field>
                  <FieldLabel htmlFor="deploy-port">SSH 端口</FieldLabel>
                  <Input
                    id="deploy-port"
                    value={port}
                    onChange={event => setPort(event.target.value)}
                    inputMode="numeric"
                  />
                </Field>
              </div>

              <Field>
                <FieldLabel htmlFor="deploy-user">登录用户名</FieldLabel>
                <Input
                  id="deploy-user"
                  value={username}
                  onChange={event => setUsername(event.target.value)}
                  placeholder="root"
                  autoComplete="off"
                />
                <FieldDescription>非 root 用户需具备 sudo 权限（免密 sudo，或在下方填写 sudo 密码）。</FieldDescription>
              </Field>

              <Field>
                <FieldLabel>认证方式</FieldLabel>
                <ToggleGroup
                  value={[authMethod]}
                  onValueChange={value => setAuthMethod((value[0] as "password" | "private_key") ?? "password")}
                  spacing={0}
                  variant="outline"
                  className="w-fit"
                >
                  <ToggleGroupItem value="password" variant="outline">
                    密码
                  </ToggleGroupItem>
                  <ToggleGroupItem value="private_key" variant="outline">
                    SSH 私钥
                  </ToggleGroupItem>
                </ToggleGroup>
              </Field>

              {authMethod === "password" ? (
                <Field>
                  <FieldLabel htmlFor="deploy-password">SSH 密码</FieldLabel>
                  <Input
                    id="deploy-password"
                    data-field="password"
                    type="password"
                    value={password}
                    onChange={event => {
                      setPassword(event.target.value)
                      setErrors(prev => (prev.password ? { ...prev, password: undefined } : prev))
                    }}
                    autoComplete="off"
                    aria-invalid={Boolean(errors.password)}
                    aria-describedby={errors.password ? "err-password" : "hint-credential"}
                  />
                  {errors.password && <FieldError id="err-password">{errors.password}</FieldError>}
                </Field>
              ) : (
                <>
                  <Field>
                    <FieldLabel htmlFor="deploy-key">SSH 私钥（PEM）</FieldLabel>
                    <Textarea
                      id="deploy-key"
                      data-field="privateKey"
                      value={privateKey}
                      onChange={event => {
                        setPrivateKey(event.target.value)
                        setErrors(prev => (prev.privateKey ? { ...prev, privateKey: undefined } : prev))
                      }}
                      rows={5}
                      spellCheck={false}
                      autoComplete="off"
                      placeholder={"-----BEGIN OPENSSH PRIVATE KEY-----\n…"}
                      className="font-mono text-xs"
                      aria-invalid={Boolean(errors.privateKey)}
                      aria-describedby={errors.privateKey ? "err-key" : "hint-credential"}
                    />
                    {errors.privateKey && <FieldError id="err-key">{errors.privateKey}</FieldError>}
                  </Field>
                  <Field>
                    <FieldLabel htmlFor="deploy-passphrase">私钥口令（可选）</FieldLabel>
                    <Input
                      id="deploy-passphrase"
                      type="password"
                      value={passphrase}
                      onChange={event => setPassphrase(event.target.value)}
                      autoComplete="off"
                    />
                  </Field>
                </>
              )}

              {username.trim() !== "root" && (
                <Field>
                  <FieldLabel htmlFor="deploy-sudo">sudo 密码（可选）</FieldLabel>
                  <Input
                    id="deploy-sudo"
                    type="password"
                    value={sudoPassword}
                    onChange={event => setSudoPassword(event.target.value)}
                    placeholder={authMethod === "password" ? "留空则复用登录密码" : "免密 sudo 可留空"}
                    autoComplete="off"
                  />
                </Field>
              )}

              <p id="hint-credential" className="text-xs text-muted-foreground">
                凭据加密后保存在本 Server，绝不会写入部署日志或审计记录。
              </p>

              <Field orientation="horizontal">
                <Checkbox
                  id="deploy-save-server"
                  checked={saveServer}
                  onCheckedChange={next => setSaveServer(Boolean(next))}
                />
                <FieldLabel htmlFor="deploy-save-server" className="text-sm font-normal">
                  保存这台服务器，便于后续重新部署
                </FieldLabel>
              </Field>
              {saveServer && (
                <Input
                  value={saveAs}
                  onChange={event => setSaveAs(event.target.value)}
                  placeholder="备注名，留空则用 IP"
                  maxLength={100}
                  aria-label="服务器备注名"
                />
              )}
            </div>
          )}
        </FieldSet>

        <FieldSeparator />

        {/* ---- 2 节点配置 ---- */}
        <FieldSet>
          <FieldLegend variant="label">
            <span className="mr-2 inline-grid size-5 place-items-center rounded-full bg-muted text-[11px] font-medium tabular-nums">
              2
            </span>
            节点配置
          </FieldLegend>

          <div className="mt-3 grid gap-4">
            <Field>
              <FieldLabel htmlFor="deploy-name">节点名称</FieldLabel>
              <Input
                id="deploy-name"
                data-field="displayName"
                value={values.displayName}
                onChange={event => set("displayName", event.target.value)}
                placeholder="如 sfu-jp-01"
                maxLength={100}
                aria-invalid={Boolean(errors.displayName)}
                aria-describedby={errors.displayName ? "err-name" : undefined}
              />
              {errors.displayName && <FieldError id="err-name">{errors.displayName}</FieldError>}
            </Field>

            <FieldSet>
              <FieldLegend variant="label" id="tls-legend">
                客户端连接的 TLS 方案
              </FieldLegend>
              <FieldDescription>
                决定客户端用什么地址连这个节点，也决定目标机上要不要安装 Caddy。
              </FieldDescription>
              <RadioGroup
                value={values.tlsMode}
                onValueChange={value => set("tlsMode", String(value) as SfuDeployTLSMode)}
                aria-labelledby="tls-legend"
                className={cn("mt-3", !isDialog && "@md/field-group:grid-cols-3")}
              >
                {TLS_OPTIONS.map(option => (
                  <FieldLabel
                    key={option.value}
                    htmlFor={`tls-${option.value}`}
                    className="transition-[background-color,border-color,box-shadow] duration-200 ease-(--modal-ease) has-data-checked:border-primary/45 has-data-checked:shadow-[0_0_0_3px_color-mix(in_oklch,var(--ring)_18%,transparent)]"
                  >
                    <Field orientation="horizontal">
                      <RadioGroupItem value={option.value} id={`tls-${option.value}`} />
                      <FieldContent>
                        <FieldTitle className="flex flex-wrap items-center gap-1.5">
                          {option.title}
                          {option.tone === "recommended" && <Badge variant="secondary">推荐</Badge>}
                          {option.tone === "danger" && <Badge variant="destructive">不安全</Badge>}
                        </FieldTitle>
                        <FieldDescription>{option.desc}</FieldDescription>
                      </FieldContent>
                    </Field>
                  </FieldLabel>
                ))}
              </RadioGroup>
            </FieldSet>

            <Collapsible open={values.tlsMode !== "none"}>
              <CollapsibleContent style={{ "--panel-open-dur": "300ms", "--panel-close-dur": "195ms" } as React.CSSProperties}>
                <Field className="pt-1">
                  <FieldLabel htmlFor="deploy-domain">域名</FieldLabel>
                  <Input
                    id="deploy-domain"
                    data-field="domain"
                    value={values.domain}
                    onChange={event => set("domain", event.target.value)}
                    placeholder="如 sfu-jp.example.com"
                    aria-invalid={Boolean(errors.domain)}
                    aria-describedby={errors.domain ? "err-domain" : "hint-domain"}
                  />
                  {errors.domain ? (
                    <FieldError id="err-domain">{errors.domain}</FieldError>
                  ) : (
                    <FieldDescription id="hint-domain">
                      {values.tlsMode === "caddy"
                        ? "需先把该域名的 A 记录指向这台服务器，且 80/443 未被占用（用于自动签发证书）。"
                        : "客户端将通过该域名的 8443 端口连接，证书需与域名匹配。"}
                    </FieldDescription>
                  )}
                </Field>
              </CollapsibleContent>
            </Collapsible>

            <Collapsible open={values.tlsMode === "custom"}>
              <CollapsibleContent style={{ "--panel-open-dur": "300ms", "--panel-close-dur": "195ms" } as React.CSSProperties}>
                <div className={cn("grid gap-4 pt-1", !isDialog && "sm:grid-cols-2")}>
                  <Field>
                    <FieldLabel htmlFor="deploy-cert">证书路径</FieldLabel>
                    <Input
                      id="deploy-cert"
                      data-field="certFile"
                      value={values.certFile}
                      onChange={event => set("certFile", event.target.value)}
                      placeholder="/etc/ssl/sfu.pem"
                      className="font-mono text-xs"
                      aria-invalid={Boolean(errors.certFile)}
                    />
                    {errors.certFile && <FieldError>{errors.certFile}</FieldError>}
                  </Field>
                  <Field>
                    <FieldLabel htmlFor="deploy-keyfile">私钥路径</FieldLabel>
                    <Input
                      id="deploy-keyfile"
                      data-field="keyFile"
                      value={values.keyFile}
                      onChange={event => set("keyFile", event.target.value)}
                      placeholder="/etc/ssl/sfu.key"
                      className="font-mono text-xs"
                      aria-invalid={Boolean(errors.keyFile)}
                    />
                    {errors.keyFile && <FieldError>{errors.keyFile}</FieldError>}
                  </Field>
                </div>
              </CollapsibleContent>
            </Collapsible>
          </div>
        </FieldSet>

        <FieldSeparator />

        {/* ---- 3 高级选项 ---- */}
        <Collapsible open={advancedOpen} onOpenChange={setAdvancedOpen}>
          <CollapsibleTrigger
            className="group/adv flex items-center gap-1.5 self-start text-sm text-muted-foreground transition-colors hover:text-foreground active:scale-[0.96]"
            render={<button type="button" />}
          >
            <ChevronDownIcon className="size-4 transition-transform duration-200 ease-(--icon-swap-ease) group-data-[panel-open]/adv:rotate-180" />
            <span className="mr-1.5 inline-grid size-5 place-items-center rounded-full bg-muted text-[11px] font-medium tabular-nums">
              3
            </span>
            高级选项
            {advancedChanges > 0 && (
              <Badge variant="secondary" className="ml-1 tabular-nums">
                {advancedChanges}
              </Badge>
            )}
          </CollapsibleTrigger>

          <CollapsibleContent>
            <div className="grid gap-4 pt-4">
              <div className={cn("grid gap-4", !isDialog && "sm:grid-cols-2")}>
                <Field>
                  <FieldLabel htmlFor="deploy-region">地域标签</FieldLabel>
                  <Input
                    id="deploy-region"
                    value={values.region}
                    onChange={event => set("region", event.target.value)}
                    placeholder="如 jp-tokyo"
                    maxLength={64}
                  />
                </Field>
                <Field>
                  <FieldLabel htmlFor="deploy-public-ip">公网 IP</FieldLabel>
                  <Input
                    id="deploy-public-ip"
                    value={values.publicIP}
                    onChange={event => set("publicIP", event.target.value)}
                    placeholder="留空自动探测"
                  />
                </Field>
                <Field>
                  <FieldLabel htmlFor="deploy-media-port">媒体 UDP 端口</FieldLabel>
                  <Input
                    id="deploy-media-port"
                    data-field="mediaUdpPort"
                    value={values.mediaUdpPort}
                    onChange={event => set("mediaUdpPort", event.target.value)}
                    inputMode="numeric"
                    aria-invalid={Boolean(errors.mediaUdpPort)}
                  />
                  {errors.mediaUdpPort && <FieldError>{errors.mediaUdpPort}</FieldError>}
                </Field>
                <Field>
                  <FieldLabel htmlFor="deploy-max-users">最大用户数</FieldLabel>
                  <Input
                    id="deploy-max-users"
                    data-field="maxUsers"
                    value={values.maxUsers}
                    onChange={event => set("maxUsers", event.target.value)}
                    inputMode="numeric"
                    aria-invalid={Boolean(errors.maxUsers)}
                  />
                  {errors.maxUsers && <FieldError>{errors.maxUsers}</FieldError>}
                </Field>
              </div>

              {releases.length > 0 && (
                <Field>
                  <FieldLabel>程序版本</FieldLabel>
                  <SimpleSelect
                    value={values.release || "__latest__"}
                    onChange={value => set("release", value === "__latest__" ? "" : value)}
                    options={[
                      { value: "__latest__", label: "自动选择最新版本" },
                      ...releases.map(r => ({ value: r.filename, label: `${r.version} · ${r.goarch}` })),
                    ]}
                    ariaLabel="程序版本"
                  />
                </Field>
              )}

              {[
                { key: "configureUFW" as const, label: "自动配置防火墙放行所需端口" },
                { key: "enableScheduling" as const, label: "上线后立即启用调度" },
                { key: "enableCascade" as const, label: "启用级联（多节点互联，放行 8843/tcp）" },
                { key: "forceReinstall" as const, label: "强制重装（覆盖该机已有的 SFU 安装）" },
                { key: "trustNewHostKey" as const, label: "信任新的主机指纹（目标机重装后勾选）" },
              ].map(item => (
                <Field key={item.key} orientation="horizontal">
                  <Checkbox
                    id={`deploy-${item.key}`}
                    checked={values[item.key]}
                    onCheckedChange={next => set(item.key, Boolean(next))}
                  />
                  <FieldLabel htmlFor={`deploy-${item.key}`} className="text-sm font-normal">
                    {item.label}
                  </FieldLabel>
                </Field>
              ))}
            </div>
          </CollapsibleContent>
        </Collapsible>
      </FieldGroup>
    </div>
  )

  const rail = (
    <DeploySummaryRail
      values={values}
      host={effectiveHost}
      username={effectiveUser}
      port={effectivePort}
      risks={risks}
      submitting={submitting}
      onSubmit={attemptSubmit}
      variant={variant}
    />
  )

  return (
    <div className={className}>
      {blockedReason && (
        <p className="mb-4 rounded-xl border border-amber-500/35 bg-amber-500/[0.06] px-3 py-2.5 text-sm text-amber-700 dark:text-amber-400">
          {blockedReason}
        </p>
      )}

      {isDialog ? (
        <div className="grid gap-4">
          {form}
          <div className="border-t pt-3">{rail}</div>
        </div>
      ) : (
        <div className="grid min-w-0 gap-6 xl:grid-cols-[minmax(0,1fr)_340px]">
          {form}
          {rail}
        </div>
      )}

      <DeployConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        host={effectiveHost}
        risks={risks}
        submitting={submitting}
        onConfirm={() => void submit()}
      />
    </div>
  )
}
