import type { JSXElement } from 'solid-js'
import type { NotificationBlock } from './notificationEntries'
import ArrowDownToLine from 'lucide-solid/icons/arrow-down-to-line'
import Bot from 'lucide-solid/icons/bot'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import TriangleAlert from 'lucide-solid/icons/triangle-alert'
import { Icon } from '~/components/common/Icon'
import { spinner } from '~/styles/animations.css'
import { MarkdownText } from './messageRenderers'
import { controlResponseMessage, resultDivider, subagentReport, subagentReportHeader } from './messageStyles.css'

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

/**
 * Draw an ordered list of notification blocks.
 *
 * A run of consecutive `text` blocks joins into ONE comma-separated paragraph, and
 * every other block is its own element. Returns null when nothing renders.
 */
export function renderNotificationBlocks(blocks: readonly NotificationBlock[]): JSXElement {
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
