/**
 * What a send does with the caret.
 *
 * `restore` puts the caret back in the editor, `release` gives the caret up so
 * the on-screen keyboard goes away, and `none` leaves focus where it is.
 */
export type SendFocusAction = 'restore' | 'release' | 'none'

export interface SendFocusInput {
  /** Whether the editor held focus when the send started. */
  hadFocus: boolean
  /** Whether the send committed. False for an empty draft and for a refused send. */
  sent: boolean
  /** Whether an on-screen keyboard covers part of the screen right now. */
  softKeyboardVisible: boolean
}

/**
 * Decide what a send does with the caret.
 *
 * A send REPAIRS the caret; it never takes one. The editor loses focus while
 * it sends -- a pressed control takes focus on Chrome and on Firefox, and
 * `replaceAll` rebuilds the document under the selection -- so the composer
 * puts the caret back afterwards. That repair is correct only when the caret
 * was in the editor to begin with. A user who dismisses the on-screen keyboard
 * and then presses Send gave the caret away on purpose, and focusing the
 * editor for them raises the keyboard again over the transcript they just
 * uncovered. That case is `none`.
 *
 * `hadFocus` decides the keyboard question too. An on-screen keyboard that is
 * up while the editor does NOT hold focus belongs to some other text field, so
 * `none` leaves that field alone rather than blurring it.
 *
 * A send that did not commit keeps the caret on every device, keyboard and
 * all: the draft is still there and the user has something to fix, so taking
 * the keyboard away would only cost them a tap back into the editor.
 */
export function decideSendFocus(input: SendFocusInput): SendFocusAction {
  if (!input.hadFocus)
    return 'none'
  if (!input.sent)
    return 'restore'
  return input.softKeyboardVisible ? 'release' : 'restore'
}
