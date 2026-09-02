import type { Component } from 'solid-js'
import type { ToolProgressEntry, ToolProgressRetry } from '~/stores/chatToolProgress'
import { createMemo, Show } from 'solid-js'
import { Tooltip } from '~/components/common/Tooltip'
import { formatDuration } from '../rendererUtils'
import * as styles from './ToolRunningBadge.css'

/**
 * The reason a subagent is retrying, for the badge's tooltip. Exported so the
 * composition rules are testable directly: the tooltip's text only reaches the
 * DOM on hover, so asserting it through a render would test the Tooltip rather
 * than this.
 *
 * `errorCategory` is the agent's own token (`overloaded`, `connection_error`).
 * The status and the delay are stated only when the agent reported them --
 * `error_status` is null for a connection error, and a zero delay means the
 * agent gave none.
 */
export function retryDetailText(retry: ToolProgressRetry): string {
  const parts = [`Retrying after ${retry.errorCategory || 'an API error'}`]
  if (retry.errorStatus !== null)
    parts.push(`HTTP ${retry.errorStatus}`)
  if (retry.retryDelayMs > 0)
    parts.push(`next attempt in ${formatDuration(retry.retryDelayMs)}`)
  return `${parts.join(' · ')}.`
}

/** The badge text for a live retry, with the reason its tooltip states. */
interface BadgeRetry {
  label: string
  detail: string
}

/**
 * What the badge displays, as plain values COPIED out of the progress entry.
 * The two retry strings travel together in one object, so neither can go absent
 * while the other is set.
 */
interface BadgeView {
  elapsedText?: string
  retry?: BadgeRetry
}

/**
 * Render one progress entry into the strings the badge shows, or undefined when
 * it shows nothing.
 *
 * The COPY is what makes the selection freeze below work at all. The store hands
 * back a live proxy, and merging a heartbeat into an entry does not change that
 * proxy's identity -- so holding the entry itself would hold a reference that
 * keeps reporting the newest value, and the freeze would do nothing. Reading the
 * fields here is also what subscribes the memo to them.
 */
function badgeView(entry: ToolProgressEntry | undefined): BadgeView | undefined {
  if (!entry)
    return undefined
  const view: BadgeView = {}
  const seconds = entry.elapsedSeconds
  // 0 renders nothing: the agent reports a whole number of seconds, so a 0 means
  // "not measured yet" rather than "instant".
  if (seconds !== undefined && seconds > 0)
    view.elapsedText = formatDuration(seconds * 1000)
  const retry = entry.retry
  if (retry) {
    view.retry = {
      label: `Retrying ${retry.attempt}/${retry.maxRetries}`,
      detail: retryDetailText(retry),
    }
  }
  return view.elapsedText === undefined && view.retry === undefined ? undefined : view
}

export interface ToolRunningBadgeProps {
  /**
   * This row's live tool progress, as a THUNK. The badge calls it; nothing above
   * it does. That is the whole point of the component: the value changes while
   * the card is on screen, and a reader higher in the tree would re-render the
   * card, which drops any text selection the user holds across it.
   */
  progress?: () => ToolProgressEntry | undefined
  /**
   * True while a document selection is live inside the chat content. The badge
   * freezes on its last value for as long as it is, because replacing this text
   * node would collapse a selection that spans it.
   */
  selectionActive?: () => boolean
}

/**
 * The "still running" badge on a tool card's header: an elapsed time, or the
 * retry state of a Task subagent that is backing off.
 *
 * It writes at most one text node per update, and the agent updates at most
 * every 30 seconds (Claude Code's heartbeat interval), so this is not a live
 * clock and there is deliberately no timer here. Interpolating between ticks
 * would mean a DOM write per second per running tool, on a row the user may be
 * selecting text in.
 */
export const ToolRunningBadge: Component<ToolRunningBadgeProps> = (props) => {
  // Read the live value FIRST, so this memo subscribes to every field badgeView
  // touches: it then re-runs when one of them changes, and again when the
  // selection ends -- at which point it returns the snapshot it has been holding
  // back. Returning `prev` while a selection is live is what makes the badge safe
  // to update under the user's cursor, and `prev` is a detached snapshot rather
  // than the store's own entry, so it cannot change while it is held.
  const shown = createMemo<BadgeView | undefined>((prev) => {
    const live = badgeView(props.progress?.())
    return props.selectionActive?.() ? prev : live
  })

  return (
    <Show when={shown()}>
      {view => (
        <Show
          when={view().retry}
          fallback={<span class={styles.root} data-testid="tool-running-badge">{view().elapsedText}</span>}
        >
          {retry => (
            <Tooltip text={retry().detail}>
              <span class={`${styles.root} ${styles.retry}`} data-testid="tool-running-badge">
                {retry().label}
              </span>
            </Tooltip>
          )}
        </Show>
      )}
    </Show>
  )
}
