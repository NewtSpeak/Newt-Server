import { useEffect, useState } from "react"
import { CrownIcon, MailPlusIcon, ShieldCheckIcon, UserRoundXIcon, Users2Icon, XIcon } from "lucide-react"
import { toast } from "sonner"

import { CopyButton } from "~/components/copy-button"
import { MemberBadgeChip, StyledName } from "~/components/server/styled-name"
import { EmptyState, ErrorState, LoadingState } from "~/components/states"
import { Avatar, AvatarFallback, AvatarImage } from "~/components/ui/avatar"
import { Badge } from "~/components/ui/badge"
import { Button } from "~/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "~/components/ui/dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from "~/components/ui/dropdown-menu"
import { Input } from "~/components/ui/input"
import { FramedAvatar } from "~/components/user-avatar-frame"
import { useAvatarFrames } from "~/hooks/use-avatar-frames"
import {
  addMemberRole,
  createInvite,
  kickMember,
  parseRoleStyle,
  removeMemberRole,
  updateMemberNickname,
  type MemberDisplay,
  type Role,
} from "~/lib/api"

function displayName(member: MemberDisplay) {
  return member.nickname || member.username || member.user_id
}

/**
 * 成员标签页：完整呈现展示自定义 —— 头像（含动态）、按角色样式渲染的用户名、
 * 徽章（悬浮显示有效期）；点击成员查看 banner/强调色等完整资料。
 */
export function MembersTab({
  guildID,
  members,
  roles,
  status,
  error,
  reload,
}: {
  guildID: string
  members: MemberDisplay[]
  roles: Role[]
  status: "idle" | "loading" | "success" | "error"
  error: string
  reload: () => void
}) {
  const [inviteCode, setInviteCode] = useState<string | null>(null)
  const [inviteOpen, setInviteOpen] = useState(false)
  const [profileMember, setProfileMember] = useState<MemberDisplay | null>(null)
  const roleByID = new Map(roles.map(role => [role.id, role]))
  // 成员列表可见用户的头像框（bot 成员不查询）
  const avatarFrames = useAvatarFrames(members.filter(member => !member.is_bot).map(member => member.user_id))

  async function onInvite() {
    try {
      const invite = await createInvite(guildID)
      setInviteCode(invite.code)
      setInviteOpen(true)
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "邀请创建失败")
    }
  }

  async function bindRole(member: MemberDisplay, roleID: string) {
    try {
      await addMemberRole(guildID, member.id, roleID)
      toast.success("角色已绑定")
      reload()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "角色绑定失败")
    }
  }

  async function unbindRole(member: MemberDisplay, roleID: string) {
    try {
      await removeMemberRole(guildID, member.id, roleID)
      toast.success("角色已解绑")
      reload()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "角色解绑失败")
    }
  }

  async function onKick(member: MemberDisplay) {
    if (!window.confirm(`确定将「${displayName(member)}」移出服务器？`)) return
    try {
      await kickMember(guildID, member.id)
      toast.success("成员已移出")
      reload()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "移出失败")
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between gap-3">
        <p className="text-sm text-muted-foreground">
          共 <span className="tabular-nums">{members.length}</span> 名成员；名字颜色/渐变来自最高层级带样式的角色，徽章悬浮可见有效期。
        </p>
        <Button variant="outline" size="sm" onClick={onInvite}>
          <MailPlusIcon data-icon="inline-start" />
          创建邀请
        </Button>
      </div>

      <Dialog open={inviteOpen} onOpenChange={setInviteOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>邀请码已生成</DialogTitle>
            <DialogDescription>
              分享链接 <code className="font-mono">/invite/{inviteCode}</code> 可直达落地页；「邀请页」标签可配置公告与协议。
            </DialogDescription>
          </DialogHeader>
          <div className="flex items-center gap-2 rounded-xl border bg-muted/40 px-4 py-3">
            <code className="flex-1 truncate font-mono text-sm">
              {typeof window !== "undefined" && inviteCode ? `${window.location.origin}/invite/${inviteCode}` : inviteCode}
            </code>
            {inviteCode && typeof window !== "undefined" && (
              <CopyButton text={`${window.location.origin}/invite/${inviteCode}`} />
            )}
          </div>
        </DialogContent>
      </Dialog>

      <MemberProfileDialog
        member={profileMember}
        roles={roles}
        guildID={guildID}
        onClose={() => setProfileMember(null)}
        onChanged={reload}
      />

      {status === "loading" && <LoadingState rows={6} />}
      {status === "error" && <ErrorState message={error} onRetry={reload} />}
      {status === "success" && members.length === 0 && (
        <EmptyState icon={Users2Icon} title="暂无成员" description="创建邀请码让其他用户加入。" />
      )}
      {status === "success" &&
        members.map((member, index) => {
          const boundIDs = new Set(member.role_ids ?? [])
          const assignable = roles.filter(role => !role.is_everyone && !boundIDs.has(role.id))
          return (
            <div
              key={member.user_id}
              style={{ "--stagger-index": index } as React.CSSProperties}
              className="anim-item flex flex-wrap items-center gap-3 rounded-xl border px-4 py-3 transition-[background-color] hover:bg-muted/40"
            >
              <button
                type="button"
                aria-label={`查看 ${displayName(member)} 的资料`}
                onClick={() => setProfileMember(member)}
                className="flex min-w-0 flex-1 items-center gap-3 text-left focus-visible:ring-3 focus-visible:ring-ring/30 focus-visible:outline-none"
              >
                <FramedAvatar frame={avatarFrames[member.user_id]}>
                  <Avatar className="size-9">
                    {member.avatar_url && <AvatarImage src={member.avatar_url} alt="" />}
                    <AvatarFallback style={member.accent_color ? { backgroundColor: `${member.accent_color}33` } : undefined}>
                      {displayName(member).slice(0, 2).toUpperCase()}
                    </AvatarFallback>
                  </Avatar>
                </FramedAvatar>
                <div className="min-w-0 flex-1">
                  <p className="flex items-center gap-1.5 truncate text-sm font-medium">
                    <StyledName nameStyle={member.name_style} className="truncate">
                      {displayName(member)}
                    </StyledName>
                    {member.is_owner && <CrownIcon className="size-3.5 shrink-0 text-amber-500" aria-label="服务器所有者" />}
                    {member.avatar_animated && (
                      <span className="rounded-sm border px-1 font-mono text-[9px] text-muted-foreground">GIF</span>
                    )}
                    {member.badges.map(badge => (
                      <MemberBadgeChip key={badge.badge_id} badge={badge} />
                    ))}
                  </p>
                  <p className="truncate font-mono text-xs text-muted-foreground">{member.user_id}</p>
                </div>
              </button>
              <div className="flex flex-wrap items-center gap-1.5">
                {(member.role_ids ?? [])
                  .map(roleID => roleByID.get(roleID))
                  .filter((role): role is Role => Boolean(role))
                  .map(role => (
                    <Badge key={role.id} variant="secondary" className="h-6 gap-1 pr-1">
                      <ShieldCheckIcon className="size-3" />
                      <StyledName nameStyle={parseRoleStyle(role.style)}>{role.name}</StyledName>
                      {!role.is_everyone && (
                        <button
                          type="button"
                          aria-label={`移除角色 ${role.name}`}
                          onClick={() => unbindRole(member, role.id)}
                          className="grid size-4 place-items-center rounded-full transition-[background-color] hover:bg-foreground/10"
                        >
                          <XIcon className="size-3" />
                        </button>
                      )}
                    </Badge>
                  ))}
                <DropdownMenu>
                  <DropdownMenuTrigger
                    render={
                      <Button variant="outline" size="xs" disabled={assignable.length === 0}>
                        + 绑定角色
                      </Button>
                    }
                  />
                  <DropdownMenuContent align="end">
                    <DropdownMenuGroup>
                      <DropdownMenuLabel>选择角色</DropdownMenuLabel>
                      {assignable.map(role => (
                        <DropdownMenuItem key={role.id} onClick={() => bindRole(member, role.id)}>
                          <ShieldCheckIcon />
                          <StyledName nameStyle={parseRoleStyle(role.style)}>{role.name}</StyledName>
                          <span className="ml-auto font-mono text-xs text-muted-foreground">P{role.position}</span>
                        </DropdownMenuItem>
                      ))}
                    </DropdownMenuGroup>
                  </DropdownMenuContent>
                </DropdownMenu>
              </div>
              <Button variant="destructive" size="icon-sm" aria-label="移出服务器" onClick={() => onKick(member)}>
                <UserRoundXIcon />
              </Button>
            </div>
          )
        })}
    </div>
  )
}

/** 成员完整资料弹窗：banner + 头像 + 强调色 + 样式名 + 徽章列表 + 昵称管理 */
function MemberProfileDialog({
  member,
  roles,
  guildID,
  onClose,
  onChanged,
}: {
  member: MemberDisplay | null
  roles: Role[]
  guildID: string
  onClose: () => void
  onChanged: () => void
}) {
  const roleByID = new Map(roles.map(role => [role.id, role]))
  const [nickname, setNickname] = useState("")
  const [savingNickname, setSavingNickname] = useState(false)
  // 弹窗内单个成员的头像框（bot 不查询；列表已预热缓存，通常直接命中）
  const avatarFrames = useAvatarFrames(member && !member.is_bot ? [member.user_id] : [])

  useEffect(() => {
    setNickname(member?.nickname ?? "")
  }, [member?.id, member?.nickname])

  async function onSaveNickname() {
    if (!member) return
    setSavingNickname(true)
    try {
      await updateMemberNickname(guildID, member.id, nickname.trim())
      toast.success(nickname.trim() ? "昵称已更新" : "昵称已清除")
      onChanged()
    } catch (reason) {
      toast.error(reason instanceof Error ? reason.message : "昵称保存失败")
    } finally {
      setSavingNickname(false)
    }
  }
  return (
    <Dialog open={member !== null} onOpenChange={open => !open && onClose()}>
      <DialogContent className="overflow-hidden p-0">
        {member && (
          <>
            <div
              className="h-24 w-full bg-muted"
              style={
                member.banner_url
                  ? { backgroundImage: `url(${member.banner_url})`, backgroundSize: "cover", backgroundPosition: "center" }
                  : member.accent_color
                    ? { backgroundColor: member.accent_color }
                    : { background: "linear-gradient(90deg, #6366f1, #8b5cf6)" }
              }
            />
            <div className="-mt-10 px-6 pb-6">
              <FramedAvatar frame={avatarFrames[member.user_id]}>
                <Avatar className="size-16 ring-4 ring-background">
                  {member.avatar_url && <AvatarImage src={member.avatar_url} alt="" />}
                  <AvatarFallback className="text-lg">{displayName(member).slice(0, 2).toUpperCase()}</AvatarFallback>
                </Avatar>
              </FramedAvatar>
              <DialogHeader className="mt-3 text-left">
                <DialogTitle className="flex items-center gap-2">
                  <StyledName nameStyle={member.name_style}>{displayName(member)}</StyledName>
                  {member.is_owner && <CrownIcon className="size-4 text-amber-500" aria-label="服务器所有者" />}
                </DialogTitle>
                <DialogDescription className="font-mono text-xs">
                  @{member.username} · {member.user_id}
                </DialogDescription>
              </DialogHeader>
              <div className="mt-4 flex flex-col gap-3 text-sm">
                <div className="flex items-end gap-2">
                  <div className="grid flex-1 gap-1.5">
                    <p className="text-xs font-medium text-muted-foreground">服务器昵称（留空清除）</p>
                    <Input
                      aria-label="服务器昵称"
                      value={nickname}
                      onChange={event => setNickname(event.target.value)}
                      maxLength={32}
                      placeholder={member.username}
                    />
                  </div>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={onSaveNickname}
                    disabled={savingNickname || nickname.trim() === (member.nickname ?? "")}
                  >
                    {savingNickname ? "保存中…" : "保存"}
                  </Button>
                </div>
                {member.badges.length > 0 && (
                  <div>
                    <p className="mb-1.5 text-xs font-medium text-muted-foreground">徽章</p>
                    <div className="flex flex-wrap gap-1.5">
                      {member.badges.map(badge => (
                        <MemberBadgeChip key={badge.badge_id} badge={badge} />
                      ))}
                    </div>
                  </div>
                )}
                <div>
                  <p className="mb-1.5 text-xs font-medium text-muted-foreground">角色</p>
                  <div className="flex flex-wrap gap-1.5">
                    {(member.role_ids ?? [])
                      .map(roleID => roleByID.get(roleID))
                      .filter((role): role is Role => Boolean(role))
                      .map(role => (
                        <Badge key={role.id} variant="secondary">
                          <StyledName nameStyle={parseRoleStyle(role.style)}>{role.name}</StyledName>
                        </Badge>
                      ))}
                    {(member.role_ids ?? []).length === 0 && <span className="text-xs text-muted-foreground">仅 @everyone</span>}
                  </div>
                </div>
                {member.accent_color && (
                  <div className="flex items-center gap-2">
                    <p className="text-xs font-medium text-muted-foreground">强调色</p>
                    <span className="size-4 rounded-full border" style={{ backgroundColor: member.accent_color }} />
                    <code className="font-mono text-xs text-muted-foreground">{member.accent_color}</code>
                  </div>
                )}
              </div>
            </div>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}
