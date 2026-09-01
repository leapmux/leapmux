import type { Component } from 'solid-js'
import type { AgentSessionSummary } from '~/generated/proto/leapmux/v1/agent_pb'
import { createMemo } from 'solid-js'
import { LoadingMenu } from '~/components/common/LoadingMenu'
import { RESUME_SESSION_ERROR_ID, RESUME_SESSION_LABEL } from '~/components/shell/resumeSession'
import { formatCompactAge } from '~/lib/dateFormat'

interface SessionSelectProps {
  /** The selected resume handle, or '' for "start a new session". */
  value: string
  onChange: (value: string) => void
  sessions: AgentSessionSummary[]
  loading: boolean
  /**
   * The field's value fails validation, so the trigger announces itself as
   * invalid and points at the field's error node.
   *
   * A handle the menu offers always validates, so this is normally false. It
   * matters because the value SURVIVES the swap from the text input: a user who
   * typed an invalid handle while the list was empty, and then changed to a
   * directory that has sessions, faces the menu with the error still showing.
   * Without this the trigger says nothing is wrong beside a disabled Create.
   *
   * A boolean rather than the message, so the picker stays ignorant of the
   * error text -- the same split `SessionIdInput` makes when it derives both
   * attributes from one `error()` read.
   */
  invalid: boolean
}

/** The row that withdraws a pick and starts a fresh session instead. */
export const NEW_SESSION_LABEL = 'Start a new session'

/**
 * The sentinel `value` of the row that hands the field back to its text box.
 *
 * `onChange` carries one value, so the field tells "start fresh" from "let me
 * type" by this value rather than by a second callback. It leads with a NUL
 * because no provider can issue a handle holding one — every handle here is a
 * session id or a filesystem path, and both exclude it — so the sentinel cannot
 * collide with a real session however the stores change.
 */
export const TYPE_A_HANDLE_VALUE = '\u0000type-a-handle'

/** The row that swaps the menu for the text box. */
export const TYPE_A_HANDLE_LABEL = 'Enter a session ID…'

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
      // Last, because it is the escape hatch rather than a session. The list
      // holds only what this worker can enumerate, so a handle from another
      // machine, one already open in a tab, and one past the worker's cap are
      // all missing from it — and without this row a directory with a single
      // session would leave no way to type any of them.
      { value: TYPE_A_HANDLE_VALUE, label: TYPE_A_HANDLE_LABEL },
    ]
  })

  return (
    <LoadingMenu
      ariaLabel={RESUME_SESSION_LABEL}
      ariaInvalid={props.invalid}
      ariaDescribedBy={props.invalid ? RESUME_SESSION_ERROR_ID : undefined}
      value={props.value}
      onChange={props.onChange}
      loadingLabel={props.loading ? 'Loading sessions...' : undefined}
      // Unreachable, and required, so it is stated rather than left to puzzle a
      // reader: `options` always holds the "start a new session" row, so
      // `LoadingMenu`'s empty state -- which tests `options.length === 0` --
      // cannot render here. The prop has no optional form, and the row is the
      // one way a user takes a pick back, so it stays at every length.
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
