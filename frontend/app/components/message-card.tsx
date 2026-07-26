// Bot 消息卡片渲染（与 Owl-Desktop message-card 对齐，按控制台惯例简化）：
// title/description/color 色条/fields/thumbnail/image/footer + 按钮行（layoutButtonRows）。
// buttons 支持外链（<a>，outline 样式 + 外链图标）与交互回调按钮三态
// idle → pending(spinner) → acked/responded(√)；INTERACTION_ACK 由本组件订阅
// 并转发进 lib/interaction-state mini-store。

import { CheckIcon, ExternalLinkIcon, Loader2Icon } from "lucide-react"
import { memo, useMemo } from "react"

import { Button, buttonVariants } from "~/components/ui/button"
import { useGatewayEvent } from "~/hooks/use-gateway"
import {
  layoutButtonRows,
  parseBotCard,
  type BotCardButton,
  type BotCardButtonSize,
  type BotCardButtonStyle,
  type BotCardInteractiveButton,
  type BotCardLinkButton,
} from "~/lib/bot-card"
import {
  applyInteractionAck,
  clickInteraction,
  useInteractionEntry,
  type InteractionAckPayload,
} from "~/lib/interaction-state"
import { cn } from "~/lib/utils"

/** card.style → ui/button variant */
const STYLE_TO_VARIANT: Record<
  BotCardButtonStyle,
  "default" | "secondary" | "success" | "destructive"
> = {
  primary: "default",
  secondary: "secondary",
  success: "success",
  danger: "destructive",
}

/** card.size → ui/button size（卡片密度下默认 sm=h-8） */
const CARD_SIZE_TO_BUTTON_SIZE: Record<
  BotCardButtonSize,
  "xs" | "sm" | "default" | "lg"
> = {
  xs: "xs",
  sm: "sm",
  md: "default",
  lg: "lg",
}

function CardLinkButton({ button }: { button: BotCardLinkButton }) {
  return (
    <a
      href={button.url}
      target="_blank"
      rel="noopener noreferrer"
      className={cn(
        buttonVariants({
          variant: "outline",
          size: CARD_SIZE_TO_BUTTON_SIZE[button.size],
        }),
        button.disabled && "pointer-events-none opacity-50"
      )}
      aria-disabled={button.disabled || undefined}
    >
      {button.label}
      <ExternalLinkIcon
        className="size-3 opacity-60"
        data-icon="inline-end"
        aria-hidden
      />
    </a>
  )
}

function CardInteractiveButton({
  button,
  messageId,
  channelId,
}: {
  button: BotCardInteractiveButton
  messageId: string
  channelId: string
}) {
  const entry = useInteractionEntry(messageId, button.customId)
  const status = entry?.status
  const busy = status === "pending" || status === "acked"
  const showCheck = status === "acked" || status === "responded"

  return (
    <Button
      type="button"
      variant={STYLE_TO_VARIANT[button.style]}
      size={CARD_SIZE_TO_BUTTON_SIZE[button.size]}
      disabled={button.disabled || busy || status === "responded"}
      aria-busy={busy || undefined}
      className={cn(busy && "opacity-80")}
      onClick={() => {
        if (!button.disabled)
          void clickInteraction(channelId, messageId, button.customId)
      }}
    >
      {status === "pending" && (
        <Loader2Icon className="size-3.5 animate-spin" aria-hidden />
      )}
      {showCheck && <CheckIcon className="t-card-check size-3.5" aria-hidden />}
      {button.label}
      {busy && (
        <span className="sr-only" aria-live="polite">
          {status === "pending" ? "正在提交" : "机器人已确认，等待响应"}
        </span>
      )}
    </Button>
  )
}

function buttonKey(button: BotCardButton, index: number): string {
  return button.kind === "interactive"
    ? `i-${button.customId}`
    : `l-${index}-${button.url}`
}

export type MessageCardProps = {
  card: unknown
  messageId: string
  channelId: string
  className?: string
}

export const MessageCard = memo(function MessageCard({
  card,
  messageId,
  channelId,
  className,
}: MessageCardProps) {
  const parsed = useMemo(() => parseBotCard(card), [card])

  // INTERACTION_ACK 定向推给点击者：转发进 mini-store 推进按钮终态
  useGatewayEvent("INTERACTION_ACK", (payload) =>
    applyInteractionAck(payload as InteractionAckPayload)
  )

  if (!parsed) return null

  const inlineFields = parsed.fields?.filter((field) => field.inline) ?? []
  const blockFields = parsed.fields?.filter((field) => !field.inline) ?? []
  const buttonRows = parsed.buttons ? layoutButtonRows(parsed.buttons) : []

  return (
    <div
      className={cn(
        "mt-1.5 max-w-md overflow-hidden rounded-lg border border-border/70 bg-muted/30 text-sm shadow-sm",
        className
      )}
      data-slot="bot-message-card"
    >
      <div className="flex min-w-0">
        <div
          className={cn(
            "w-1 shrink-0 self-stretch",
            !parsed.color && "bg-primary"
          )}
          style={parsed.color ? { backgroundColor: parsed.color } : undefined}
          aria-hidden
        />
        <div className="min-w-0 flex-1 p-3">
          <div className="flex gap-3">
            <div className="min-w-0 flex-1 space-y-1.5">
              {parsed.title && (
                <p className="leading-snug font-semibold text-balance text-foreground">
                  {parsed.title}
                </p>
              )}
              {parsed.description && (
                <p className="text-[13px] leading-relaxed break-words whitespace-pre-wrap text-muted-foreground">
                  {parsed.description}
                </p>
              )}

              {inlineFields.length > 0 && (
                <div className="grid grid-cols-2 gap-x-3 gap-y-2 pt-1 sm:grid-cols-3">
                  {inlineFields.map((field, index) => (
                    <div
                      key={`inline-${index}-${field.name}`}
                      className="min-w-0"
                    >
                      <p className="truncate text-[11px] font-semibold text-muted-foreground">
                        {field.name}
                      </p>
                      <p className="text-[13px] break-words text-foreground">
                        {field.value}
                      </p>
                    </div>
                  ))}
                </div>
              )}

              {blockFields.length > 0 && (
                <div className="space-y-2 pt-1">
                  {blockFields.map((field, index) => (
                    <div
                      key={`block-${index}-${field.name}`}
                      className="min-w-0"
                    >
                      <p className="text-[11px] font-semibold text-muted-foreground">
                        {field.name}
                      </p>
                      <p className="text-[13px] break-words whitespace-pre-wrap text-foreground">
                        {field.value}
                      </p>
                    </div>
                  ))}
                </div>
              )}
            </div>

            {parsed.thumbnail && (
              <img
                src={parsed.thumbnail}
                alt=""
                className="size-16 shrink-0 rounded-md object-cover outline-1 -outline-offset-1 outline-black/10 dark:outline-white/10"
                loading="lazy"
                referrerPolicy="no-referrer"
              />
            )}
          </div>

          {parsed.image && (
            <img
              src={parsed.image}
              alt=""
              className="mt-2 max-h-56 w-full rounded-md object-cover outline-1 -outline-offset-1 outline-black/10 dark:outline-white/10"
              loading="lazy"
              referrerPolicy="no-referrer"
            />
          )}

          {buttonRows.length > 0 && (
            <div className="mt-2.5 space-y-1.5">
              {buttonRows.map((row, rowIndex) => (
                <div
                  key={`button-row-${rowIndex}`}
                  className="flex flex-wrap gap-1.5"
                >
                  {row.map((button, index) =>
                    button.kind === "link" ? (
                      <CardLinkButton
                        key={buttonKey(button, index)}
                        button={button}
                      />
                    ) : (
                      <CardInteractiveButton
                        key={buttonKey(button, index)}
                        button={button}
                        messageId={messageId}
                        channelId={channelId}
                      />
                    )
                  )}
                </div>
              ))}
            </div>
          )}

          {parsed.footer && (
            <p className="mt-2 text-[11px] text-muted-foreground">
              {parsed.footer}
            </p>
          )}
        </div>
      </div>
    </div>
  )
})
