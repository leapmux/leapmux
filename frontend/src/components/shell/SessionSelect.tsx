import type { Component } from 'solid-js'
import type { LoadingMenuOptionDetail } from '~/components/common/LoadingMenu'
import type { AgentSessionSummary } from '~/generated/proto/leapmux/v1/agent_pb'
import { createMemo } from 'solid-js'
import { RelativeTimeAgo } from '~/components/chat/RelativeTime'
import { LoadingMenu } from '~/components/common/LoadingMenu'
import { RESUME_SESSION_ERROR_ID, RESUME_SESSION_LABEL, typeAHandleLabel } from '~/components/shell/resumeSession'
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
  /**
   * The selected provider's session is a FILE, so its handle may be a path.
   *
   * It reaches only the row that hands the field to its text box, which is the
   * one sentence a user reads BEFORE that box exists: for Pi it has to invite a
   * path as well as an ID, exactly as the placeholder inside the box does.
   */
  isFilePath: boolean
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

/**
 * Build one menu row's label.
 *
 * The handle stands in for a missing title rather than a placeholder like
 * "Untitled": Pi stores no title at all and a Claude session has none until its
 * title pass runs, and a handle the user can recognise beats a word that
 * describes every such row identically.
 */
export function sessionOptionLabel(session: AgentSessionSummary): string {
  return session.title.trim() || session.sessionId
}

/**
 * Build one menu row's trailing note: how long ago the session last ran.
 *
 * A DETAIL and not part of the label, although the label used to carry it. A
 * title has no length limit, and a row clips its label with an ellipsis -- so
 * an age inside the label was cut off first on exactly the rows where two
 * attempts at one task can be told apart by nothing else. The detail column
 * never shrinks.
 *
 * `text` keeps the filter working over the age. `LoadingMenu` matches the
 * detail beside the label, so typing `d` still narrows to sessions from days
 * ago. It is read at FILTER time and against the same clock the element draws
 * from, so the two can never state different ages; the element beside it is
 * what the reader sees.
 *
 * `RelativeTimeAgo` supplies that element, so a row states the age in the form
 * every timestamp in the app takes, and hovering it gives the full local date
 * and time. It parses the timestamp again and renders nothing for an
 * unparseable one -- the same rule as the guard here, which is why an
 * unparseable timestamp produces no detail at all rather than an empty column.
 */
export function sessionOptionDetail(
  session: AgentSessionSummary,
): LoadingMenuOptionDetail | undefined {
  const updated = session.updatedAt ? new Date(session.updatedAt) : null
  if (updated === null || Number.isNaN(updated.getTime()))
    return undefined
  return {
    // Read fresh at filter time, against the SAME clock the element beside it
    // draws from: `formatCompactAge`'s own default `new Date()`. As a frozen
    // string it was measured at whatever instant the options memo last ran,
    // while `RelativeTimeAgo` kept ticking -- so a row that read "4h ago" was
    // matched against "3h ago" and typing what was on screen emptied the list.
    text: () => `${formatCompactAge(updated)} ago`,
    render: () => <RelativeTimeAgo timestamp={session.updatedAt} />,
  }
}

/**
 * The resume-session picker: a filtered menu over the sessions the worker
 * offered.
 *
 * Each row states a title and an age, in two columns rather than one string.
 * The title clips with an ellipsis and gives the rest back on hover; the age
 * keeps its place at the right end however long the title is.
 */
export const SessionSelect: Component<SessionSelectProps> = (props) => {
  // A memo, and it is load-bearing: `LoadingMenu`'s `visible` memo re-reads
  // `options` on every filter keystroke and its `<For>` reconciles by
  // reference, so a plain `.map()` here would rebuild every row per keystroke.
  // `BranchSelect` documents the same constraint.
  const options = createMemo(() => {
    return [
      // A real, selectable choice — not the synthetic prompt row `BranchSelect`
      // was corrected for injecting. `placeholder` covers the untouched state;
      // this is how a user takes a pick BACK.
      // `pinned`, like the row below it: both are ways to LEAVE the list, so a
      // query that narrows the list must not take them with it.
      { value: '', label: NEW_SESSION_LABEL, pinned: true },
      // Second, beside the other row that is not a session. The two of them are
      // the ways to leave the list, and the list itself runs to the worker's
      // cap: at the bottom this row sat under a scroll the user had to reach the
      // end of, which is the wrong place for the answer to "my session is not
      // here". It is needed exactly because the list holds only what this worker
      // can enumerate — a handle from another machine, one already open in a
      // tab, and one past the cap are all missing from it.
      { value: TYPE_A_HANDLE_VALUE, label: typeAHandleLabel(props.isFilePath), pinned: true },
      ...props.sessions.map(session => ({
        value: session.sessionId,
        label: sessionOptionLabel(session),
        detail: sessionOptionDetail(session),
      })),
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
