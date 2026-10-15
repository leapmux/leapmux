import type { JSXElement } from 'solid-js'
import type { AutoReviewOutcome } from './model/notification'
import type { NotificationBlock } from './notificationEntries'
import type { RowRenderContext } from './renderContext'
import ArrowDownToLine from 'lucide-solid/icons/arrow-down-to-line'
import Ban from 'lucide-solid/icons/ban'
import Bot from 'lucide-solid/icons/bot'
import Clock from 'lucide-solid/icons/clock'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import Shield from 'lucide-solid/icons/shield'
import ShieldCheck from 'lucide-solid/icons/shield-check'
import ShieldX from 'lucide-solid/icons/shield-x'
import TriangleAlert from 'lucide-solid/icons/triangle-alert'
import { createSignal, Show } from 'solid-js'
import { compactControl } from '~/components/common/CompactControl.css'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { spinner } from '~/styles/animations.css'
import { MarkdownText } from './messageRenderers'
import { autoReview, autoReviewActions, autoReviewHeader, autoReviewHeaderDenied, controlResponseMessage, resultDivider, subagentReport, subagentReportHeader } from './messageStyles.css'
import { AUTO_REVIEW_APPROVE_LABEL, AUTO_REVIEW_APPROVE_TOOLTIP, AUTO_REVIEW_APPROVED_LABEL } from './notificationEntries'

// The markup half of the notification pipeline. Every decision about WHAT a row says
// lives in `notificationEntries.ts`; this file decides only how the blocks look.

/**
 * A labelled full-width rule: a leading glyph followed by the label, drawn with the
 * same `resultDivider` style as a turn-end divider. Every `divider` block flows
 * through here, so a boundary looks the same on its own, consolidated, and across
 * providers.
 *
 * A loading divider uses a spinner. A completed divider uses the compaction arrow.
 */
function NotificationDivider(props: { text: string, loading?: boolean }): JSXElement {
  return (
    <div class={resultDivider} data-testid="notification-divider">
      <Icon
        icon={props.loading ? LoaderCircle : ArrowDownToLine}
        size="sm"
        {...(props.loading ? { class: spinner } : {})}
      />
      {` ${props.text}`}
    </div>
  )
}

const AUTO_REVIEW_ICONS: Record<AutoReviewOutcome, typeof Shield> = {
  'approved': ShieldCheck,
  'denied': ShieldX,
  'timed-out': Clock,
  'aborted': Ban,
  'unknown': Shield,
}

/**
 * One automatic review, and the reader's approval of a denial.
 *
 * A recorded approval shows "Approved" from the row's metadata.
 * It remains visible after reload and needs no live approval capability.
 * Otherwise, the control appears only while the current context accepts approval.
 * The context reads the capability through a getter, so it follows agent startup and shutdown.
 * An accepted click shows approval before the updated row arrives.
 * After refusal, the control enables again and the caller shows an error toast.
 */
function AutoReviewBlock(props: {
  block: Extract<NotificationBlock, { kind: 'auto-review' }>
  context: RowRenderContext | undefined
}): JSXElement {
  const [approval, setApproval] = createSignal<'idle' | 'pending' | 'approved'>('idle')
  const approved = () => props.block.approved || approval() === 'approved'
  const approve = async () => {
    const reviews = props.context?.reviews
    if (reviews === undefined || approved() || approval() !== 'idle')
      return
    setApproval('pending')
    try {
      await reviews.approveDenied(props.block.reviewId)
      setApproval('approved')
    }
    catch {
      setApproval('idle')
    }
  }
  return (
    <div class={autoReview} data-testid="auto-review" data-outcome={props.block.outcome}>
      <div class={props.block.outcome === 'denied' ? `${autoReviewHeader} ${autoReviewHeaderDenied}` : autoReviewHeader}>
        <Icon icon={AUTO_REVIEW_ICONS[props.block.outcome]} size="sm" />
        {props.block.title}
      </div>
      <Show when={props.block.rationale}>
        {rationale => <MarkdownText text={rationale()} />}
      </Show>
      <Show when={approved() || (props.block.approvable && props.context?.reviews !== undefined)}>
        <div class={autoReviewActions}>
          <Show
            when={approved()}
            fallback={(
              <Tooltip text={AUTO_REVIEW_APPROVE_TOOLTIP}>
                <button
                  type="button"
                  class={`${compactControl} outline`}
                  data-testid="auto-review-approve"
                  disabled={approval() === 'pending'}
                  onClick={() => void approve()}
                >
                  {AUTO_REVIEW_APPROVE_LABEL}
                </button>
              </Tooltip>
            )}
          >
            <span data-testid="auto-review-approved">{AUTO_REVIEW_APPROVED_LABEL}</span>
          </Show>
        </div>
      </Show>
    </div>
  )
}

/**
 * Draw an ordered list of notification blocks.
 *
 * A run of consecutive `text` blocks joins into ONE comma-separated paragraph, and
 * every other block is its own element. Returns null when nothing renders. `context`
 * supplies the capabilities that an interactive block uses.
 */
export function renderNotificationBlocks(blocks: readonly NotificationBlock[], context?: RowRenderContext): JSXElement {
  const elements: JSXElement[] = []
  let pendingText: string[] = []

  const flushPendingText = () => {
    if (pendingText.length === 0)
      return
    elements.push(<div class={controlResponseMessage}>{pendingText.join(', ')}</div>)
    pendingText = []
  }

  for (const block of blocks) {
    if (block.kind === 'text') {
      pendingText.push(block.text)
      continue
    }
    flushPendingText()
    if (block.kind === 'auto-review') {
      elements.push(<AutoReviewBlock block={block} context={context} />)
      continue
    }
    if (block.kind === 'subagent-report') {
      const warning = block.status === 'flagged'
      const withheld = block.status === 'withheld'
      const verb = warning ? 'reported — security warning' : withheld ? 'report withheld' : 'reported'
      elements.push(
        <div class={subagentReport}>
          <div class={subagentReportHeader}>
            <Icon icon={warning || withheld ? TriangleAlert : Bot} size="sm" />
            {`${block.label || 'Subagent'} ${verb}`}
          </div>
          <MarkdownText text={block.text} />
        </div>,
      )
      continue
    }
    elements.push(
      <NotificationDivider
        text={block.text}
        {...(block.loading !== undefined ? { loading: block.loading } : {})}
      />,
    )
  }
  flushPendingText()

  if (elements.length === 0)
    return null
  if (elements.length === 1)
    return elements[0]
  return <div>{elements}</div>
}
