import type { SendFocusAction, SendFocusInput } from './sendFocus'
import { describe, expect, it } from 'vitest'
import { decideSendFocus } from './sendFocus'

describe('decideSendFocus', () => {
  // The full matrix, so a change to one branch cannot quietly move another.
  const cases: Array<[SendFocusInput, SendFocusAction]> = [
    // The caret was in the editor and the send committed: put the caret back on
    // a desktop, and give it up when an on-screen keyboard is in the way.
    [{ hadFocus: true, sent: true, softKeyboardVisible: false }, 'restore'],
    [{ hadFocus: true, sent: true, softKeyboardVisible: true }, 'release'],

    // The send did not commit, so the draft is still there to fix. The caret
    // stays, and the on-screen keyboard with it.
    [{ hadFocus: true, sent: false, softKeyboardVisible: false }, 'restore'],
    [{ hadFocus: true, sent: false, softKeyboardVisible: true }, 'restore'],

    // The caret was somewhere else. A send never takes one, whatever else is
    // true -- this is the row that keeps the on-screen keyboard down after a
    // press on Send from a composer the user had already left.
    [{ hadFocus: false, sent: true, softKeyboardVisible: false }, 'none'],
    [{ hadFocus: false, sent: true, softKeyboardVisible: true }, 'none'],
    [{ hadFocus: false, sent: false, softKeyboardVisible: false }, 'none'],
    [{ hadFocus: false, sent: false, softKeyboardVisible: true }, 'none'],
  ]

  it.each(cases)('%j gives %s', (input, expected) => {
    expect(decideSendFocus(input)).toBe(expected)
  })

  it('never releases a keyboard that belongs to another field', () => {
    // An on-screen keyboard is up while the editor does NOT hold focus, so it
    // came up for some other text field. Blurring the active element there
    // would take the caret out of a field the user is still using.
    expect(decideSendFocus({ hadFocus: false, sent: true, softKeyboardVisible: true })).not.toBe('release')
  })
})
