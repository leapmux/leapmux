/**
 * @vitest-environment jsdom
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { PRESS_SLOP_PX } from '~/components/common/contextMenuGesture'
import { motion } from '~/styles/tokens'
import { inputOrEditableHosts, popoverHost } from '~/test-support/embeddedUi'
import { pointerEvent } from '~/test-support/pointer'
import { attachDismissSoftKeyboardOnTap } from './dismissSoftKeyboardOnTap'
import { setSoftKeyboardVisible } from './softKeyboard'

function attach<T extends HTMLElement>(el: T): T {
  document.body.appendChild(el)
  return el
}

function focusedEditor(): HTMLTextAreaElement {
  setSoftKeyboardVisible(true)
  const editor = attach(document.createElement('textarea'))
  editor.focus()
  expect(document.activeElement).toBe(editor)
  return editor
}

function tap(target: EventTarget, opts: { x?: number, y?: number, pointerId?: number, isPrimary?: boolean } = {}) {
  const down = { x: opts.x ?? 40, y: opts.y ?? 40, pointerId: opts.pointerId, isPrimary: opts.isPrimary, pointerType: 'touch' }
  target.dispatchEvent(pointerEvent('pointerdown', down))
  target.dispatchEvent(pointerEvent('pointerup', down))
}

let stop: (() => void) | undefined

afterEach(() => {
  stop?.()
  stop = undefined
  vi.useRealTimers()
  setSoftKeyboardVisible(false)
  document.body.innerHTML = ''
})

describe('attachDismissSoftKeyboardOnTap', () => {
  it('releases a focused editor on a short still tap on a plain surface', () => {
    const editor = focusedEditor()
    const surface = attach(document.createElement('div'))
    stop = attachDismissSoftKeyboardOnTap()

    tap(surface)

    expect(document.activeElement).not.toBe(editor)
  })

  it('keeps the caret when no keyboard is taking screen space', () => {
    setSoftKeyboardVisible(false)
    const editor = attach(document.createElement('textarea'))
    editor.focus()
    const surface = attach(document.createElement('div'))
    stop = attachDismissSoftKeyboardOnTap()

    tap(surface)

    expect(document.activeElement).toBe(editor)
  })

  it('keeps the caret when the tap lands inside every text-entry host', () => {
    const editor = focusedEditor()
    stop = attachDismissSoftKeyboardOnTap()

    for (const { label, host, target } of inputOrEditableHosts()) {
      attach(host)
      tap(target)
      expect(document.activeElement, label).toBe(editor)
    }
  })

  it('keeps the caret when the tap lands on a control this gesture owns', () => {
    const editor = focusedEditor()
    stop = attachDismissSoftKeyboardOnTap()

    const own: { label: string, host: HTMLElement, target: Element }[] = []
    for (const tag of ['a', 'button', 'select'] as const) {
      const host = document.createElement(tag)
      own.push({ label: `<${tag}>`, host, target: host })
    }
    const roleButton = document.createElement('div')
    roleButton.setAttribute('role', 'button')
    own.push({ label: '[role="button"]', host: roleButton, target: roleButton })
    const labeled = document.createElement('button')
    const label = document.createElement('span')
    labeled.appendChild(label)
    own.push({ label: 'span inside <button>', host: labeled, target: label })
    own.push(popoverHost())

    for (const { label, host, target } of own) {
      attach(host)
      tap(target)
      expect(document.activeElement, label).toBe(editor)
    }
  })

  it('ignores a press that travelled past the hold slop', () => {
    const editor = focusedEditor()
    const surface = attach(document.createElement('div'))
    stop = attachDismissSoftKeyboardOnTap()

    surface.dispatchEvent(pointerEvent('pointerdown', { x: 40, y: 40, pointerType: 'touch' }))
    surface.dispatchEvent(pointerEvent('pointerup', { x: 40, y: 40 + PRESS_SLOP_PX + 1, pointerType: 'touch' }))

    expect(document.activeElement).toBe(editor)
  })

  it('still dismisses a press that travelled exactly the hold slop', () => {
    const editor = focusedEditor()
    const surface = attach(document.createElement('div'))
    stop = attachDismissSoftKeyboardOnTap()

    surface.dispatchEvent(pointerEvent('pointerdown', { x: 40, y: 40, pointerType: 'touch' }))
    surface.dispatchEvent(pointerEvent('pointerup', { x: 40, y: 40 + PRESS_SLOP_PX, pointerType: 'touch' }))

    expect(document.activeElement).not.toBe(editor)
  })

  it('ignores a press that lasted as long as a long press', () => {
    vi.useFakeTimers({ toFake: ['performance', 'Date'] })
    const editor = focusedEditor()
    const surface = attach(document.createElement('div'))
    stop = attachDismissSoftKeyboardOnTap()

    surface.dispatchEvent(pointerEvent('pointerdown', { x: 40, y: 40, pointerType: 'touch' }))
    vi.advanceTimersByTime(motion.longPress)
    surface.dispatchEvent(pointerEvent('pointerup', { x: 40, y: 40, pointerType: 'touch' }))

    expect(document.activeElement).toBe(editor)
  })

  it('ignores a secondary finger', () => {
    const editor = focusedEditor()
    const surface = attach(document.createElement('div'))
    stop = attachDismissSoftKeyboardOnTap()

    tap(surface, { isPrimary: false, pointerId: 2 })

    expect(document.activeElement).toBe(editor)
  })

  it('ignores a pointerup that does not match the press', () => {
    const editor = focusedEditor()
    const surface = attach(document.createElement('div'))
    stop = attachDismissSoftKeyboardOnTap()

    surface.dispatchEvent(pointerEvent('pointerdown', { x: 40, y: 40, pointerId: 1, pointerType: 'touch' }))
    surface.dispatchEvent(pointerEvent('pointerup', { x: 40, y: 40, pointerId: 2, pointerType: 'touch' }))

    expect(document.activeElement).toBe(editor)
  })

  it('drops a cancelled press so a later lift does not dismiss', () => {
    const editor = focusedEditor()
    const surface = attach(document.createElement('div'))
    stop = attachDismissSoftKeyboardOnTap()

    surface.dispatchEvent(pointerEvent('pointerdown', { x: 40, y: 40, pointerType: 'touch' }))
    surface.dispatchEvent(pointerEvent('pointercancel', { x: 40, y: 40, pointerType: 'touch' }))
    surface.dispatchEvent(pointerEvent('pointerup', { x: 40, y: 40, pointerType: 'touch' }))

    expect(document.activeElement).toBe(editor)
  })

  it('treats a tap on a text node as a tap on its parent', () => {
    const editor = focusedEditor()
    const surface = attach(document.createElement('div'))
    const text = document.createTextNode('hello')
    surface.appendChild(text)
    stop = attachDismissSoftKeyboardOnTap()

    tap(text)

    expect(document.activeElement).not.toBe(editor)
  })

  it('stops dismissing after the disposer runs', () => {
    const editor = focusedEditor()
    const surface = attach(document.createElement('div'))
    const dispose = attachDismissSoftKeyboardOnTap()
    dispose()

    tap(surface)

    expect(document.activeElement).toBe(editor)
  })

  // xterm focuses its helper from a compatibility `mousedown` that, on
  // touch, arrives after our `pointerup` dismiss. Swallow that focus so a
  // tap on the terminal hides the keyboard instead of hiding it and
  // showing it again.
  it('releases an editor that the same tap focuses after the dismiss', () => {
    const editor = focusedEditor()
    const surface = attach(document.createElement('div'))
    const terminalInput = attach(document.createElement('textarea'))
    stop = attachDismissSoftKeyboardOnTap()

    tap(surface)
    expect(document.activeElement).not.toBe(editor)

    terminalInput.focus()
    expect(document.activeElement).not.toBe(terminalInput)
  })

  it('lets an editor take focus on a tap while the keyboard is already down', () => {
    setSoftKeyboardVisible(false)
    const surface = attach(document.createElement('div'))
    const terminalInput = attach(document.createElement('textarea'))
    stop = attachDismissSoftKeyboardOnTap()

    tap(surface)
    terminalInput.focus()

    expect(document.activeElement).toBe(terminalInput)
  })

  it('lets an editor take focus on the next tap after a dismiss', () => {
    const editor = focusedEditor()
    const surface = attach(document.createElement('div'))
    const terminalInput = attach(document.createElement('textarea'))
    stop = attachDismissSoftKeyboardOnTap()

    tap(surface)
    expect(document.activeElement).not.toBe(editor)

    setSoftKeyboardVisible(false)
    tap(surface)
    terminalInput.focus()

    expect(document.activeElement).toBe(terminalInput)
  })

  it('does not swallow later focus after a tap on a control', () => {
    const editor = focusedEditor()
    const button = attach(document.createElement('button'))
    const terminalInput = attach(document.createElement('textarea'))
    stop = attachDismissSoftKeyboardOnTap()

    tap(button)
    expect(document.activeElement).toBe(editor)

    terminalInput.focus()
    expect(document.activeElement).toBe(terminalInput)
  })

  it('does not blur a control that takes focus during the swallow', () => {
    focusedEditor()
    const surface = attach(document.createElement('div'))
    const button = attach(document.createElement('button'))
    stop = attachDismissSoftKeyboardOnTap()

    tap(surface)
    button.focus()

    expect(document.activeElement).toBe(button)
  })

  it('keeps swallowing when a secondary finger lands', () => {
    focusedEditor()
    const surface = attach(document.createElement('div'))
    const terminalInput = attach(document.createElement('textarea'))
    stop = attachDismissSoftKeyboardOnTap()

    tap(surface)
    surface.dispatchEvent(pointerEvent('pointerdown', { isPrimary: false, pointerId: 2, pointerType: 'touch' }))
    terminalInput.focus()

    expect(document.activeElement).not.toBe(terminalInput)
  })

  it('stops swallowing focus after the disposer runs', () => {
    focusedEditor()
    const surface = attach(document.createElement('div'))
    const terminalInput = attach(document.createElement('textarea'))
    const dispose = attachDismissSoftKeyboardOnTap()

    tap(surface)
    dispose()
    terminalInput.focus()

    expect(document.activeElement).toBe(terminalInput)
  })
})
