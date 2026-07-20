// RBAC 64 位权限位表 —— 依据 docs/设计讨论/2026-07-20-02-RBAC权限位与计算规则.md §3.2
// 分组：服务器 / 文本 / 语音 / 舞台 / 共享。位 46–63 保留，禁止使用。

export type PermissionBit = {
  bit: number
  name: string
  label: string
  description?: string
  /** 首期必实现子集（P0） */
  p0?: boolean
}

export type PermissionGroup = {
  id: "guild" | "text" | "voice" | "stage" | "stream"
  label: string
  bits: PermissionBit[]
}

export const PERMISSION_GROUPS: PermissionGroup[] = [
  {
    id: "guild",
    label: "服务器",
    bits: [
      { bit: 3, name: "ADMINISTRATOR", label: "管理员", description: "拥有全部权限，无视频道拒绝覆盖", p0: true },
      { bit: 5, name: "MANAGE_GUILD", label: "管理服务器", description: "修改服务器设置、图标、区域等", p0: true },
      { bit: 4, name: "MANAGE_CHANNELS", label: "管理频道", description: "创建、编辑、删除频道", p0: true },
      { bit: 28, name: "MANAGE_ROLES", label: "管理角色", description: "管理角色与频道权限覆盖", p0: true },
      { bit: 10, name: "VIEW_CHANNEL", label: "查看频道", description: "频道可见与读取的前提", p0: true },
      { bit: 0, name: "CREATE_INSTANT_INVITE", label: "创建邀请", p0: true },
      { bit: 1, name: "KICK_MEMBERS", label: "踢出成员", p0: true },
      { bit: 2, name: "BAN_MEMBERS", label: "封禁成员", p0: true },
      { bit: 39, name: "MODERATE_MEMBERS", label: "管理限制（禁言）", description: "对成员施加多维限制", p0: true },
      { bit: 26, name: "CHANGE_NICKNAME", label: "修改自己昵称", p0: true },
      { bit: 27, name: "MANAGE_NICKNAMES", label: "管理他人昵称", p0: true },
      { bit: 7, name: "VIEW_AUDIT_LOG", label: "查看审计日志" },
      { bit: 19, name: "VIEW_GUILD_INSIGHTS", label: "查看服务器分析" },
      { bit: 52, name: "MANAGE_BOTS", label: "管理机器人", description: "创建/配置/授权本服机器人集成" },
      { bit: 29, name: "MANAGE_WEBHOOKS", label: "管理 Webhook" },
      { bit: 30, name: "MANAGE_EXPRESSIONS", label: "管理表情与贴纸" },
      { bit: 33, name: "MANAGE_EVENTS", label: "管理活动" },
    ],
  },
  {
    id: "text",
    label: "文本",
    bits: [
      { bit: 11, name: "SEND_MESSAGES", label: "发送消息", p0: true },
      { bit: 16, name: "READ_MESSAGE_HISTORY", label: "查看历史消息", p0: true },
      { bit: 13, name: "MANAGE_MESSAGES", label: "管理消息", description: "删除他人消息、置顶等", p0: true },
      { bit: 15, name: "ATTACH_FILES", label: "上传附件", p0: true },
      { bit: 6, name: "ADD_REACTIONS", label: "添加表情回应" },
      { bit: 14, name: "EMBED_LINKS", label: "嵌入链接预览" },
      { bit: 17, name: "MENTION_EVERYONE", label: "提及 @everyone" },
      { bit: 18, name: "USE_EXTERNAL_EMOJIS", label: "使用外部表情" },
      { bit: 12, name: "SEND_TTS_MESSAGES", label: "发送 TTS 消息" },
      { bit: 31, name: "USE_APPLICATION_COMMANDS", label: "使用应用命令" },
      { bit: 34, name: "MANAGE_THREADS", label: "管理帖子" },
      { bit: 35, name: "CREATE_PUBLIC_THREADS", label: "创建公开帖子" },
      { bit: 36, name: "CREATE_PRIVATE_THREADS", label: "创建私密帖子" },
      { bit: 38, name: "SEND_MESSAGES_IN_THREADS", label: "在帖子中发消息" },
      { bit: 37, name: "USE_EXTERNAL_STICKERS", label: "使用外部贴纸" },
      { bit: 45, name: "SEND_VOICE_MESSAGES", label: "发送语音条消息" },
    ],
  },
  {
    id: "voice",
    label: "语音",
    bits: [
      { bit: 20, name: "CONNECT", label: "连接语音频道", p0: true },
      { bit: 21, name: "SPEAK", label: "语音发言", p0: true },
      { bit: 22, name: "MUTE_MEMBERS", label: "静音他人", p0: true },
      { bit: 23, name: "DEAFEN_MEMBERS", label: "聋麦他人", p0: true },
      { bit: 24, name: "MOVE_MEMBERS", label: "移动成员", p0: true },
      { bit: 8, name: "PRIORITY_SPEAKER", label: "优先发言" },
      { bit: 25, name: "USE_VAD", label: "语音活动检测" },
      { bit: 41, name: "USE_SOUNDBOARD", label: "使用音效板" },
      { bit: 44, name: "USE_EXTERNAL_SOUNDS", label: "使用外部音效" },
    ],
  },
  {
    id: "stage",
    label: "舞台",
    bits: [{ bit: 32, name: "REQUEST_TO_SPEAK", label: "申请上麦", description: "舞台模式下加入申请队列" }],
  },
  {
    id: "stream",
    label: "共享",
    bits: [{ bit: 9, name: "STREAM", label: "屏幕共享 / 直播", description: "发起屏幕共享（后期必做，位已预留）" }],
  },
]

export const ALL_PERMISSION_BITS: PermissionBit[] = PERMISSION_GROUPS.flatMap(group => group.bits)

export function hasBit(mask: number, bit: number) {
  // 位 ≤45，Number 精度（2^53）内安全
  return Math.floor(mask / 2 ** bit) % 2 === 1
}

export function setBit(mask: number, bit: number, on: boolean) {
  const has = hasBit(mask, bit)
  if (on && !has) return mask + 2 ** bit
  if (!on && has) return mask - 2 ** bit
  return mask
}

export function describePermissions(mask: number) {
  return ALL_PERMISSION_BITS.filter(item => hasBit(mask, item.bit))
}
