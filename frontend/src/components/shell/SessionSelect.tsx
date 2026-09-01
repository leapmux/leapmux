import type { Component } from 'solid-js'
import type { AgentSessionSummary } from '~/generated/proto/leapmux/v1/agent_pb'
import { createMemo } from 'solid-js'
import { LoadingMenu } from '~/components/common/LoadingMenu'
import { formatCompactAge } from '~/lib/dateFormat'

interface SessionSelectProps {
  /** The selected resume handle, or '' for "start a new session". */
  value: string
  onChange: (value: string) => void
  sessions: AgentSessionSummary[]
  loading: boolean
}

/** The row that withdraws a pick and starts a fresh session instead. */
export const NEW_SESSION_LABEL = 'Start a new session'

/**
 * Build one menu row's label.
 *
 * The handle stands in for a missing title rather than a placeholder like
 * "Untitled": Pi stores no title at all and a Claude session has none until its
 * title pass runs, and a handle the user can recognise beats a word that
 * describes every such row identically.
 *
 * The age is part of the LABEL, not a second column, because `LoadingMenu`
 * filters by substring over the label — so with it there, typing `d` narrows to
 * sessions from days ago as well as to titles containing a `d`.
 */
export function sessionOptionLabel(session: AgentSessionSummary, now: Date): string {
  const title = session.title.trim() || session.sessionId
  const updated = session.updatedAt ? new Date(session.updatedAt) : null
  if (updated === null || Number.isNaN(updated.getTime()))
    return title
  return `${title} — ${formatCompactAge(updated, now)} ago`
}

/**
 * The resume-session picker: a filtered menu over the sessions the worker
 * offered.
 *
 * The label does NOT tick the way `RelativeTime` does. The options memo
 * recomputes only when the session list changes, so an age shown here is fixed
 * for as long as the dialog stays open — correct for a control that lives for
 * seconds, and it keeps a menu out of `createSharedTicker`'s subscriber list.
 */
export const SessionSelect: Component<SessionSelectProps> = (props) => {
  // A memo, and it is load-bearing: `LoadingMenu`'s `visible` memo re-reads
  // `options` on every filter keystroke and its `<For>` reconciles by
  // reference, so a plain `.map()` here would rebuild every row per keystroke.
  // `BranchSelect` documents the same constraint.
  const options = createMemo(() => {
    // One instant for the whole list, so two rows recorded a second apart
    // cannot be measured against two different "now"s.
    const now = new Date()
    return [
      // A real, selectable choice — not the synthetic prompt row `BranchSelect`
      // was corrected for injecting. `placeholder` covers the untouched state;
      // this is how a user takes a pick BACK.
      { value: '', label: NEW_SESSION_LABEL },
      ...props.sessions.map(session => ({
        value: session.sessionId,
        label: sessionOptionLabel(session, now),
      })),
    ]
  })

  return (
    <LoadingMenu
      ariaLabel="Resume an existing session"
      value={props.value}
      onChange={props.onChange}
      loadingLabel={props.loading ? 'Loading sessions...' : undefined}
      emptyLabel="No sessions found"
      placeholder={NEW_SESSION_LABEL}
      // Explicit, because `LoadingMenu` would otherwise derive it from the
      // option count and only offer a filter box past a dozen entries. This
      // list runs to the worker's cap, and finding one session by eye is the
      // work the filter removes at any length.
      filter
      options={options()}
      data-testid="session-select-menu"
    />
  )
}
