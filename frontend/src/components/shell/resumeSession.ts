/**
 * The visible label of the resume field, reused as the accessible name of
 * whichever control the field shows.
 *
 * ONE name for both controls. `ResumeSessionField` swaps between `SessionSelect`
 * and `SessionIdInput` when the session list arrives, and a screen-reader user
 * whose label changed at that moment would be told the field became a different
 * field.
 */
export const RESUME_SESSION_LABEL = 'Resume an existing session'

/**
 * The id of the field's validation-error node, so whichever control is mounted
 * can point at it with `aria-describedby`.
 *
 * `ResumeSessionField` renders the node and both controls name it. The two
 * controls never render together, so one id serves both and cannot collide.
 */
export const RESUME_SESSION_ERROR_ID = 'session-id-error'

/**
 * The placeholder of the text box.
 *
 * Declared beside the label of the menu row that OPENS that text box, because
 * the two state one fact: which shapes of handle the selected provider accepts.
 * Pi's session is a FILE, so it takes a path as well as an ID; every other
 * provider issues an opaque token. While the row read "Enter a session ID…" for
 * Pi, the one sentence a user reads BEFORE choosing named the wrong set, and the
 * box it opened then contradicted it.
 *
 * Two functions rather than one phrase, because the two sentences differ in
 * grammar and not in fact: a placeholder is a noun phrase, and a menu row is an
 * instruction.
 */
export function resumeHandlePlaceholder(isFilePath: boolean): string {
  return isFilePath ? 'Session ID or file path' : 'Session ID'
}

/** The label of the menu row that swaps the menu for the text box. */
export function typeAHandleLabel(isFilePath: boolean): string {
  return isFilePath ? 'Enter a session ID or a file path…' : 'Enter a session ID…'
}
