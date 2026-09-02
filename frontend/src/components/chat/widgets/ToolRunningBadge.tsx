import type { Component } from 'solid-js'
import type { ToolProgressEntry, ToolProgressRetry } from '~/stores/chatToolProgress'
import { createMemo, Show } from 'solid-js'
import { Tooltip } from '~/components/common/Tooltip'
import { formatDuration, formatSecondsParts } from '../rendererUtils'
import * as styles from './ToolRunningBadge.css'

/**
 * The agent's error-category token as words. The token is snake_case on the wire
 * (`server_error`, `connection_error`), and this sentence reaches a user, so the
 * underscores go.
 *
 * The mapping stays generic on purpose. Each provider owns its own set of
 * category tokens, so a table of one provider's tokens does not belong in this
 * shared widget.
 */
function retryReasonText(errorCategory: string): string {
  return errorCategory.replaceAll('_', ' ').trim() || 'an API error'
}

/**
 * The reason a subagent retries, for the badge's tooltip. Exported so the
 * composition rules are testable directly: the tooltip's text only reaches the
 * DOM on hover, so an assertion through a render would test the Tooltip rather
 * than this.
 *
 * The status and the delay appear only when the agent reported them --
 * `error_status` is null for a failure that carried no HTTP status, and a zero
 * delay means the agent gave none.
 */
export function retryDetailText(retry: ToolProgressRetry): string {
  const parts = [`Retrying after ${retryReasonText(retry.errorCategory)}`]
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
 * back a live proxy, and a merge of a heartbeat into an entry does not change
 * that proxy's identity -- so a hold on the entry itself keeps a reference that
 * reports the newest value, and the freeze does nothing. The reads here are also
 * what subscribe the memo to those fields.
 */
function badgeView(entry: ToolProgressEntry | undefined): BadgeView | undefined {
  if (!entry)
    return undefined
  const view: BadgeView = {}
  const seconds = entry.elapsedSeconds
  // 0 renders nothing: the agent reports a whole number of seconds, so a 0 means
  // "not measured yet" rather than "instant".
  if (seconds !== undefined && seconds > 0)
    view.elapsedText = formatSecondsParts(seconds)
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
   * the card is on screen, and a reader higher in the tree re-renders the card,
   * which drops any text selection the user holds across it.
   */
  toolProgress?: () => ToolProgressEntry | undefined
  /**
   * True while a document selection is live inside the chat content. The badge
   * then freezes on its last value, because a replacement of this text node
   * collapses a selection that spans it.
   */
  textSelectionActive?: () => boolean
}

/**
 * The "still running" badge on a tool card's header: an elapsed time, or the
 * retry state of a Task subagent that backs off.
 *
 * It writes at most one text node per update, and the agent updates at most
 * every 30 seconds (Claude Code's heartbeat interval), so this is not a live
 * clock and there is deliberately no timer here. Interpolation between the ticks
 * costs a DOM write per second per running tool, on a row where the user may
 * select text.
 *
 * The elapsed time therefore steps 30, 60, 90, and only Claude's MAIN agent
 * reports one at all -- so a tool inside a subagent, a tool under any other
 * provider, and a tool shorter than 30 seconds all show nothing. Deriving the
 * value from the tool_use row's own timestamp would close every one of those
 * gaps and would reverse the no-timer decision above:
 * https://github.com/leapmux/leapmux/issues/439
 */
export const ToolRunningBadge: Component<ToolRunningBadgeProps> = (props) => {
  // Read the live value FIRST, so this memo subscribes to every field badgeView
  // touches: it then re-runs when one of them changes, and again when the
  // selection ends -- at which point it returns the snapshot it held back.
  // Returning `prev` while a selection is live is what makes the badge safe to
  // update under the user's cursor, and `prev` is a detached snapshot rather
  // than the store's own entry, so it cannot change while the memo holds it.
  //
  // The freeze needs a `prev` to hold. On the FIRST run there is none, and a
  // chat-wide selection is often already live when the virtualizer mounts a row
  // -- the user selects text, then scrolls. Without the `prev !== undefined`
  // guard the badge returns undefined and renders nothing for the whole life of
  // that selection, instead of freezing on a value. The seeded first paint is
  // safe for the reason the freeze exists: it INSERTS a node rather than
  // replacing one, so it disturbs no selection.
  const shown = createMemo<BadgeView | undefined>((prev) => {
    const live = badgeView(props.toolProgress?.())
    return props.textSelectionActive?.() && prev !== undefined ? prev : live
  })

  // ONE span, and one Tooltip that is always mounted. The retry state changes
  // the span's class and its text, never its identity -- a second <Show> branch
  // would build a fresh span and a fresh text node when a retry starts or
  // resolves, which is exactly the node replacement that collapses a selection
  // across the badge. An empty `text` disables the Tooltip (see TooltipProps),
  // and ClippedText, RelativeTime and IconButton all mount one unconditionally
  // for the same reason.
  return (
    <Show when={shown()}>
      {view => (
        <Tooltip text={view().retry?.detail ?? ''}>
          <span
            class={view().retry ? `${styles.root} ${styles.retry}` : styles.root}
            data-testid="tool-running-badge"
          >
            {view().retry?.label ?? view().elapsedText}
          </span>
        </Tooltip>
      )}
    </Show>
  )
}
