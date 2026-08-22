/**
 * @vitest-environment jsdom
 */
import { afterEach, describe, expect, it } from 'vitest'
import { dismissSoftKeyboard, isSoftKeyboardTarget, isSoftKeyboardVisible, setSoftKeyboardVisible } from './softKeyboard'

function attach<T extends HTMLElement>(el: T): T {
  document.body.appendChild(el)
  return el
}

afterEach(() => {
  // Module-level state: leave it where a fresh page would.
  setSoftKeyboardVisible(false)
  document.body.innerHTML = ''
})

describe('isSoftKeyboardTarget', () => {
  it('matches the elements the on-screen keyboard comes up for', () => {
    expect(isSoftKeyboardTarget(document.createElement('input'))).toBe(true)
    expect(isSoftKeyboardTarget(document.createElement('textarea'))).toBe(true)
    for (const spelling of ['true', '', 'plaintext-only']) {
      const host = document.createElement('div')
      host.setAttribute('contenteditable', spelling)
      expect(isSoftKeyboardTarget(host), `contenteditable="${spelling}"`).toBe(true)
    }
  })

  it('does not match an element that takes no typing', () => {
    expect(isSoftKeyboardTarget(null)).toBe(false)
    expect(isSoftKeyboardTarget(document.createElement('button'))).toBe(false)
    expect(isSoftKeyboardTarget(document.createElement('div'))).toBe(false)
    const off = document.createElement('div')
    off.setAttribute('contenteditable', 'false')
    expect(isSoftKeyboardTarget(off)).toBe(false)
  })

  it('does not match a select, a checkbox, or a button input', () => {
    expect(isSoftKeyboardTarget(document.createElement('select'))).toBe(false)
    const checkbox = document.createElement('input')
    checkbox.type = 'checkbox'
    expect(isSoftKeyboardTarget(checkbox)).toBe(false)
    const button = document.createElement('input')
    button.type = 'button'
    expect(isSoftKeyboardTarget(button)).toBe(false)
  })
})

describe('isSoftKeyboardVisible', () => {
  it('starts false and follows what the viewport watcher publishes', () => {
    expect(isSoftKeyboardVisible()).toBe(false)
    setSoftKeyboardVisible(true)
    expect(isSoftKeyboardVisible()).toBe(true)
    setSoftKeyboardVisible(false)
    expect(isSoftKeyboardVisible()).toBe(false)
  })
})

describe('dismissSoftKeyboard', () => {
  it('releases a focused editable while a keyboard covers the screen', () => {
    setSoftKeyboardVisible(true)
    const input = attach(document.createElement('input'))
    input.focus()
    expect(document.activeElement).toBe(input)

    dismissSoftKeyboard()

    expect(document.activeElement).not.toBe(input)
  })

  // The hardware-keyboard case, and the desktop one. A coarse pointer does NOT
  // imply an on-screen keyboard: a tablet with a paired keyboard reports one
  // and shows no keyboard, and dropping the caret there reclaims nothing.
  it('keeps the caret when no keyboard is taking screen space', () => {
    setSoftKeyboardVisible(false)
    const input = attach(document.createElement('input'))
    input.focus()

    dismissSoftKeyboard()

    expect(document.activeElement).toBe(input)
  })

  // It must not steal focus from a control the user just moved to.
  it('leaves focus alone when it sits on something that takes no typing', () => {
    setSoftKeyboardVisible(true)
    const button = attach(document.createElement('button'))
    button.focus()

    dismissSoftKeyboard()

    expect(document.activeElement).toBe(button)
  })

  it('does nothing when nothing holds focus', () => {
    setSoftKeyboardVisible(true)
    expect(() => dismissSoftKeyboard()).not.toThrow()
  })
})
