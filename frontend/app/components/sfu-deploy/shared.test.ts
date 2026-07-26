/// <reference types="bun" />
import { describe, expect, test } from "bun:test"

import {
  collectRisks,
  countAdvancedChanges,
  DEFAULT_FORM,
  deriveAdvertiseURL,
  deriveStepStates,
  diagnose,
  formToRequest,
  hasBlockingRisk,
  linesToText,
  paramsToForm,
  parseLog,
  STEPS,
  validateForm,
} from "./shared"
import type { SfuDeployment } from "~/lib/api"

function deployment(overrides: Partial<SfuDeployment> = {}): SfuDeployment {
  return {
    id: "d1",
    host: "203.0.113.10",
    port: 22,
    username: "root",
    status: "RUNNING",
    step: "CONNECTING",
    params: {},
    created_by: "u1",
    created_at: "2026-07-26T10:00:00Z",
    updated_at: "2026-07-26T10:01:00Z",
    ...overrides,
  }
}

describe("parseLog", () => {
  test("按前缀分级，并把编排器消息识别为 notice", () => {
    const lines = parseLog(
      ["正在连接 root@1.2.3.4", "[+] 检测到 Ubuntu 22.04", "[!] 未检测到 ufw", "[x] 端口 443 已被占用"].join("\n")
    )
    expect(lines.map(l => l.level)).toEqual(["notice", "info", "warn", "error"])
    expect(lines.map(l => l.n)).toEqual([1, 2, 3, 4])
    expect(lines[1].prefix).toBe("[+]")
    expect(lines[1].body).toBe("检测到 Ubuntu 22.04")
    // 编排器消息无前缀
    expect(lines[0].prefix).toBe("")
  })

  test("识别截断提示为 meta 级", () => {
    const lines = parseLog("…（较早日志已截断）\n[+] 继续\n")
    expect(lines[0].level).toBe("meta")
    expect(lines).toHaveLength(2)
  })

  test("末尾换行不产生空行", () => {
    expect(parseLog("a\nb\n")).toHaveLength(2)
    expect(parseLog("")).toHaveLength(0)
  })

  test("往返：解析后还原回原始文本", () => {
    const raw = "开始部署\n[+] 就绪\n[x] 失败"
    expect(linesToText(parseLog(raw))).toBe(raw)
  })
})

describe("deriveAdvertiseURL", () => {
  test("三种 TLS 模式各自的地址形态", () => {
    expect(deriveAdvertiseURL({ tls_mode: "caddy", domain: "sfu.example.com" })).toBe(
      "wss://sfu.example.com/ws"
    )
    expect(deriveAdvertiseURL({ tls_mode: "custom", domain: "sfu.example.com" })).toBe(
      "wss://sfu.example.com:8443/ws"
    )
    expect(deriveAdvertiseURL({ tls_mode: "none", public_ip: "203.0.113.10" })).toBe(
      "ws://203.0.113.10:8443/ws"
    )
  })

  test("缺少必要输入时返回空串而非半截地址", () => {
    expect(deriveAdvertiseURL({ tls_mode: "caddy", domain: "" })).toBe("")
    expect(deriveAdvertiseURL({ tls_mode: "none" })).toBe("")
  })

  test("明文模式在没有 public_ip 时回落到 SSH 主机", () => {
    expect(deriveAdvertiseURL({ tls_mode: "none", host: "198.51.100.7" })).toBe(
      "ws://198.51.100.7:8443/ws"
    )
  })
})

describe("deriveStepStates", () => {
  test("进行中：已过步骤 done、当前 active、其余 pending", () => {
    const states = deriveStepStates(deployment({ step: "INSTALL_DEPS" }))
    expect(states[0]).toBe("done")
    expect(states[1]).toBe("done")
    expect(states[2]).toBe("active")
    expect(states[3]).toBe("pending")
  })

  test("失败时当前步骤标记为 failed", () => {
    const states = deriveStepStates(deployment({ status: "FAILED", step: "CONFIGURE" }))
    const index = STEPS.findIndex(s => s.key === "CONFIGURE")
    expect(states[index]).toBe("failed")
    expect(states[index + 1]).toBe("pending")
  })

  test("未开启调度时该步骤为 skipped 而非 done", () => {
    const states = deriveStepStates(
      deployment({ status: "SUCCEEDED", step: "DONE", params: { enable_scheduling: false } })
    )
    const index = STEPS.findIndex(s => s.key === "ENABLE_SCHEDULING")
    expect(states[index]).toBe("skipped")
    expect(states[STEPS.length - 1]).toBe("done")
  })

  test("开启调度时成功后全部 done", () => {
    const states = deriveStepStates(
      deployment({ status: "SUCCEEDED", step: "DONE", params: { enable_scheduling: true } })
    )
    expect(states.every(s => s === "done")).toBe(true)
  })
})

describe("collectRisks", () => {
  const base = {
    tlsMode: "caddy" as const,
    domain: "sfu.example.com",
    host: "203.0.113.10",
    mediaUdpPort: 3478,
    forceReinstall: false,
    trustNewHostKey: false,
    configureUFW: false,
    enableScheduling: false,
    enableCascade: false,
  }

  test("强制重装与明文 TLS 都是阻断级风险", () => {
    expect(hasBlockingRisk(collectRisks({ ...base, forceReinstall: true }))).toBe(true)
    expect(hasBlockingRisk(collectRisks({ ...base, tlsMode: "none", domain: "" }))).toBe(true)
  })

  test("仅 caddy 提示时不构成阻断", () => {
    const risks = collectRisks(base)
    expect(risks.map(r => r.key)).toEqual(["caddy"])
    expect(hasBlockingRisk(risks)).toBe(false)
  })

  test("防火墙风险列出实际放行的端口", () => {
    const risks = collectRisks({ ...base, configureUFW: true, mediaUdpPort: 4000, enableCascade: true })
    const ufw = risks.find(r => r.key === "ufw")
    expect(ufw?.text).toContain("4000/udp")
    expect(ufw?.text).toContain("8843/tcp")
  })

  test("阻断级风险排在警告与提示之前", () => {
    const risks = collectRisks({
      ...base,
      forceReinstall: true,
      configureUFW: true,
      enableScheduling: true,
    })
    expect(risks[0].tone).toBe("danger")
    expect(risks[risks.length - 1].tone).toBe("info")
  })
})

describe("diagnose", () => {
  test("按失败步骤与关键词给出可执行建议", () => {
    const result = diagnose(
      deployment({ status: "FAILED", step: "CONNECTING", error: "主机密钥指纹变更，可能存在中间人攻击" })
    )
    expect(result?.actions.some(a => a.includes("信任新的主机指纹"))).toBe(true)
  })

  test("WAIT_ONLINE 无需关键词即可匹配", () => {
    const result = diagnose(deployment({ status: "FAILED", step: "WAIT_ONLINE", error: "超时" }))
    expect(result?.cause).toContain("回连")
  })

  test("非失败状态不给诊断", () => {
    expect(diagnose(deployment({ status: "SUCCEEDED" }))).toBeNull()
    expect(diagnose(null)).toBeNull()
  })

  test("步骤限定生效：CONNECTING 的规则不会命中 PRECHECK", () => {
    const result = diagnose(deployment({ status: "FAILED", step: "PRECHECK", error: "sudo 提权失败" }))
    expect(result?.actions.some(a => a.includes("sudo"))).toBe(true)
  })
})

describe("paramsToForm", () => {
  test("还原历史配置用于重试", () => {
    const form = paramsToForm({
      display_name: "东京-1",
      labels: { region: "jp" },
      tls_mode: "custom",
      domain: "sfu.example.com",
      tls_cert_file: "/etc/ssl/a.pem",
      media_udp_port: 4000,
      max_users: 500,
      enable_cascade: true,
      enable_scheduling: false,
      configure_ufw: false,
    })
    expect(form.displayName).toBe("东京-1")
    expect(form.region).toBe("jp")
    expect(form.tlsMode).toBe("custom")
    expect(form.mediaUdpPort).toBe("4000")
    expect(form.maxUsers).toBe("500")
    expect(form.enableCascade).toBe(true)
    expect(form.enableScheduling).toBe(false)
    expect(form.configureUFW).toBe(false)
  })

  test("指纹信任不继承，每次都要显式确认", () => {
    expect(paramsToForm({ trust_new_hostkey: true }).trustNewHostKey).toBe(false)
  })

  test("空参数与非法 tls_mode 回落默认值", () => {
    expect(paramsToForm(undefined)).toEqual(DEFAULT_FORM)
    expect(paramsToForm({ tls_mode: "bogus" }).tlsMode).toBe("caddy")
  })
})

describe("formToRequest", () => {
  test("custom 之外的模式不下发证书路径", () => {
    const request = formToRequest({ ...DEFAULT_FORM, tlsMode: "caddy", certFile: "/x", keyFile: "/y" })
    expect(request.node.tls_cert_file).toBeUndefined()
    expect(request.node.tls_key_file).toBeUndefined()
  })

  test("空地域标签不生成 labels", () => {
    expect(formToRequest({ ...DEFAULT_FORM, region: "  " }).node.labels).toBeUndefined()
    expect(formToRequest({ ...DEFAULT_FORM, region: "jp" }).node.labels).toEqual({ region: "jp" })
  })

  test("端口与人数转成数字", () => {
    const request = formToRequest({ ...DEFAULT_FORM, mediaUdpPort: "4000", maxUsers: "500" })
    expect(request.node.media_udp_port).toBe(4000)
    expect(request.node.max_users).toBe(500)
  })
})

describe("validateForm", () => {
  const conn = { useSaved: false, host: "1.2.3.4", password: "pw", privateKey: "", authMethod: "password" }

  test("默认表单只缺节点名与域名", () => {
    const errors = validateForm(DEFAULT_FORM, conn)
    expect(errors.displayName).toBeTruthy()
    expect(errors.domain).toBeTruthy()
    expect(errors.host).toBeUndefined()
  })

  test("填齐后通过", () => {
    const errors = validateForm({ ...DEFAULT_FORM, displayName: "n", domain: "a.example.com" }, conn)
    expect(Object.values(errors).filter(Boolean)).toHaveLength(0)
  })

  test("明文模式不要求域名", () => {
    const errors = validateForm({ ...DEFAULT_FORM, displayName: "n", tlsMode: "none" }, conn)
    expect(errors.domain).toBeUndefined()
  })

  test("使用已存服务器时不校验连接字段", () => {
    const errors = validateForm(
      { ...DEFAULT_FORM, displayName: "n", tlsMode: "none" },
      { useSaved: true, host: "", password: "", privateKey: "", authMethod: "password" }
    )
    expect(errors.host).toBeUndefined()
    expect(errors.password).toBeUndefined()
  })

  test("私钥登录缺私钥时报错", () => {
    const errors = validateForm(
      { ...DEFAULT_FORM, displayName: "n", tlsMode: "none" },
      { useSaved: false, host: "1.2.3.4", password: "", privateKey: "", authMethod: "private_key" }
    )
    expect(errors.privateKey).toBeTruthy()
  })

  test("端口越界与非正整数人数被拦下", () => {
    const errors = validateForm(
      { ...DEFAULT_FORM, displayName: "n", tlsMode: "none", mediaUdpPort: "70000", maxUsers: "0" },
      conn
    )
    expect(errors.mediaUdpPort).toBeTruthy()
    expect(errors.maxUsers).toBeTruthy()
  })
})

describe("countAdvancedChanges", () => {
  test("默认值不计入", () => {
    expect(countAdvancedChanges(DEFAULT_FORM)).toBe(0)
  })

  test("统计被改动的高级字段，不含基础字段", () => {
    expect(
      countAdvancedChanges({ ...DEFAULT_FORM, displayName: "改了名字但不算高级项" })
    ).toBe(0)
    expect(
      countAdvancedChanges({ ...DEFAULT_FORM, region: "jp", forceReinstall: true, maxUsers: "500" })
    ).toBe(3)
  })
})
