/**
 * The visible label of the resume field, reused as the accessible name of
 * whichever control the field is showing.
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
