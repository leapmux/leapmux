import type { EditorRef } from '~/stores/editorRef.store'
import { renderHook } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { registerEditorRef, unregisterEditorRef } from '~/stores/editorRef.store'
import { useChatAutoFocus } from './useChatAutoFocus'

const AGENT_ID = 'agent-1'

/** An editor ref that records what the hook inserts into it. */
function fakeEditor(): { ref: EditorRef, inserted: string[] } {
  const inserted: string[] = []
  return {
    inserted,
    ref: {
      get: () => '',
      set: vi.fn(),
      focus: vi.fn(),
      insert: (text: string) => {
        inserted.push(text)
      },
    },
  }
}

/**
 * Mount the hook with an editor registered for `agentId`.
 *
 * Returns the inserted text plus the cleanup, so each test asserts on what
 * the focused editor received rather than on the hook's return value — the
 * hook returns nothing.
 */
function mount(agentId: string | null = AGENT_ID) {
  const editor = fakeEditor()
  registerEditorRef(AGENT_ID, editor.ref)
  const { cleanup } = renderHook(() => useChatAutoFocus(() => agentId))
  return { inserted: editor.inserted, cleanup }
}

/** Dispatch a keydown on the document and report whether it was consumed. */
function press(key: string, init: KeyboardEventInit = {}): boolean {
  const event = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...init })
  document.dispatchEvent(event)
  return event.defaultPrevented
}

/** Focus `el` inside the document body. */
function focus(el: HTMLElement): HTMLElement {
  document.body.append(el)
  el.focus()
  // The branch under test is reached only while focus really landed, so an
  // element that refuses focus fails here and not on the assertion.
  expect(document.activeElement).toBe(el)
  return el
}

/** An element that carries `contenteditable` set to `value`. */
function editable(value: string): HTMLElement {
  const el = document.createElement('div')
  el.setAttribute('contenteditable', value)
  return el
}

describe('useChatAutoFocus', () => {
  afterEach(() => {
    unregisterEditorRef(AGENT_ID)
    document.body.replaceChildren()
  })

  it('inserts a plain character into the focused agent editor', () => {
    const { inserted, cleanup } = mount()
    expect(press('a')).toBe(true)
    expect(inserted).toEqual(['a'])
    cleanup()
  })

  it('ignores a key that carries a modifier', () => {
    const { inserted, cleanup } = mount()
    expect(press('a', { ctrlKey: true })).toBe(false)
    expect(press('a', { metaKey: true })).toBe(false)
    expect(press('a', { altKey: true })).toBe(false)
    expect(inserted).toEqual([])
    cleanup()
  })

  // Shift is the exception: it is how the user types a capital letter.
  it('inserts a shifted character', () => {
    const { inserted, cleanup } = mount()
    expect(press('A', { shiftKey: true })).toBe(true)
    expect(inserted).toEqual(['A'])
    cleanup()
  })

  it('ignores a named key and the space that scrolls the page', () => {
    const { inserted, cleanup } = mount()
    expect(press('Enter')).toBe(false)
    expect(press('ArrowDown')).toBe(false)
    expect(press(' ')).toBe(false)
    expect(inserted).toEqual([])
    cleanup()
  })

  it('does nothing while no agent tab holds focus', () => {
    const { inserted, cleanup } = mount(null)
    expect(press('a')).toBe(false)
    expect(inserted).toEqual([])
    cleanup()
  })

  it('does nothing when the focused agent has no editor registered', () => {
    const { cleanup } = mount('agent-without-editor')
    expect(press('a')).toBe(false)
    cleanup()
  })

  it('leaves the key to a focused input, textarea or select', () => {
    const { inserted, cleanup } = mount()
    for (const tag of ['input', 'textarea', 'select'] as const) {
      focus(document.createElement(tag))
      expect(press('a')).toBe(false)
    }
    expect(inserted).toEqual([])
    cleanup()
  })

  it('leaves the key to a focused contenteditable element', () => {
    const { inserted, cleanup } = mount()
    focus(editable('true'))
    expect(press('a')).toBe(false)
    expect(inserted).toEqual([])
    cleanup()
  })

  // `contenteditable=""` is the HTML shorthand for the true state and
  // `plaintext-only` is what a plain-text editing host uses. Both take the
  // user's keystrokes, so stealing the key into the chat editor drops a
  // character out of what the user typed.
  it('leaves the key to the empty and plaintext-only spellings', () => {
    const { inserted, cleanup } = mount()
    focus(editable(''))
    expect(press('a')).toBe(false)
    document.body.replaceChildren()
    focus(editable('plaintext-only'))
    expect(press('b')).toBe(false)
    expect(inserted).toEqual([])
    cleanup()
  })

  it('takes the key from a contenteditable="false" element', () => {
    const { inserted, cleanup } = mount()
    focus(editable('false'))
    expect(press('a')).toBe(true)
    expect(inserted).toEqual(['a'])
    cleanup()
  })

  it('leaves the key to a control inside an open dialog or a menu', () => {
    const { inserted, cleanup } = mount()
    for (const markup of ['<dialog open><button></button></dialog>', '<div role="menu"><button></button></div>', '<div role="listbox"><button></button></div>']) {
      const host = document.createElement('div')
      host.innerHTML = markup
      document.body.append(host)
      const button = host.querySelector('button')!
      button.focus()
      expect(document.activeElement).toBe(button)
      expect(press('a')).toBe(false)
      document.body.replaceChildren()
    }
    expect(inserted).toEqual([])
    cleanup()
  })

  it('stops inserting once the hook is cleaned up', () => {
    const { inserted, cleanup } = mount()
    cleanup()
    expect(press('a')).toBe(false)
    expect(inserted).toEqual([])
  })
})
