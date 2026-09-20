import type { LucideIcon } from 'lucide-solid'
import type { JSXElement } from 'solid-js'
import type { NotificationIconHint } from './model/notification'
import type { NotificationBlock } from './notificationEntries'
import ArrowDownToLine from 'lucide-solid/icons/arrow-down-to-line'
import Check from 'lucide-solid/icons/check'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import OctagonMinus from 'lucide-solid/icons/octagon-minus'
import RotateCcw from 'lucide-solid/icons/rotate-ccw'
import X from 'lucide-solid/icons/x'
import { Icon } from '~/components/common/Icon'
import { spinner } from '~/styles/animations.css'
import { controlResponseMessage, resultDivider } from './messageStyles.css'

// The markup half of the notification pipeline. Every decision about WHAT a row says
// lives in `notificationEntries.ts`; this file decides only how the blocks look.

/**
 * The glyph for each divider outcome the model states.
 *
 * Exhaustive over {@link NotificationIconHint}, so a new outcome fails to
 * compile until this file gives it a glyph. A `stopped` subagent and one that
 * merely ENDED share the octagon: the second states no outcome, and inventing
 * a distinct glyph for it would claim one.
 */
const DIVIDER_ICON: Record<NotificationIconHint, LucideIcon> = {
  succeeded: Check,
  failed: X,
  stopped: OctagonMinus,
  interrupted: RotateCcw,
}

/**
 * A labelled full-width rule: a leading glyph followed by the label, drawn with the
 * same `resultDivider` style as a turn-end divider. Every `divider` block flows
 * through here, so a boundary looks the same on its own, consolidated, and across
 * providers.
 *
 * The glyph is the spinner while `loading`, else the block's own `icon`, else the
 * compaction arrow (the original and still most common divider).
 */
function NotificationDivider(props: { text: string, loading?: boolean, icon?: NotificationIconHint }): JSXElement {
  return (
    <div class={resultDivider}>
      <Icon
        icon={props.loading ? LoaderCircle : props.icon ? DIVIDER_ICON[props.icon] : ArrowDownToLine}
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
    elements.push(
      <NotificationDivider
        text={block.text}
        {...(block.loading !== undefined ? { loading: block.loading } : {})}
        {...(block.icon !== undefined ? { icon: block.icon } : {})}
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
