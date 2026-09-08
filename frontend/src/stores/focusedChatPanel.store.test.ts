import { afterEach, describe, expect, it, vi } from 'vitest'
import { getActiveChatPanel, getFocusedChatPanel, registerChatPanel, unregisterChatPanel } from './focusedChatPanel.store'

afterEach(() => {
  document.body.innerHTML = ''
})

/** A panel element with one focusable child, registered with the given handle. */
function mountPanel(handle: { send?: () => void, hasPendingInput?: () => boolean } = {}) {
  const panel = document.createElement('div')
  panel.setAttribute('data-chat-panel', '')
  const input = document.createElement('input')
  panel.appendChild(input)
  document.body.appendChild(panel)
  const send = handle.send ?? vi.fn()
  const hasPendingInput = handle.hasPendingInput ?? (() => false)
  registerChatPanel(panel, { send, hasPendingInput })
  return { panel, input, send, hasPendingInput }
}

describe('focusedChatPanel store', () => {
  it('returns the handle for the panel containing the focused element', () => {
    const { input, send } = mountPanel()
    input.focus()

    expect(getFocusedChatPanel()?.send).toBe(send)
  })

  it('returns undefined when focus is outside any chat panel', () => {
    mountPanel()
    const outside = document.createElement('input')
    document.body.appendChild(outside)
    outside.focus()

    expect(getFocusedChatPanel()).toBeUndefined()
  })

  it('resolves the panel that actually contains the focused element when multiple are registered', () => {
    const a = mountPanel()
    const b = mountPanel()

    b.input.focus()
    expect(getFocusedChatPanel()?.send).toBe(b.send)

    a.input.focus()
    expect(getFocusedChatPanel()?.send).toBe(a.send)
  })

  it('returns undefined after unregister', () => {
    const { panel, input } = mountPanel()
    input.focus()
    unregisterChatPanel(panel)

    expect(getFocusedChatPanel()).toBeUndefined()
  })

  // The steer action reads the CURRENT agent tab's composer, which is not
  // necessarily the one holding focus -- the user may read the transcript.
  describe('the active panel', () => {
    it('answers without focus being inside it', () => {
      const { send } = mountPanel()
      const outside = document.createElement('input')
      document.body.appendChild(outside)
      outside.focus()

      expect(getFocusedChatPanel()).toBeUndefined()
      expect(getActiveChatPanel()?.send).toBe(send)
    })

    it('is the panel that registered most recently', () => {
      mountPanel()
      const second = mountPanel()

      expect(getActiveChatPanel()?.send).toBe(second.send)
    })

    it('replaces the handle when one panel registers again', () => {
      const { panel } = mountPanel()
      const replacement = vi.fn()
      registerChatPanel(panel, { send: replacement, hasPendingInput: () => true })

      expect(getActiveChatPanel()?.send).toBe(replacement)
      expect(getActiveChatPanel()?.hasPendingInput()).toBe(true)
    })

    it('returns undefined after unregister', () => {
      const { panel } = mountPanel()
      unregisterChatPanel(panel)

      expect(getActiveChatPanel()).toBeUndefined()
    })

    // The guard against registering a snapshot boolean. The composer's own
    // `hasContent` signal lags the document by a 200 ms debounce, so a handle
    // that captured a value would report an empty composer for a fifth of a
    // second after every keystroke -- exactly when the send chord is pressed.
    it('re-reads hasPendingInput on every call, so a freshly typed character counts', () => {
      let typed = false
      mountPanel({ hasPendingInput: () => typed })

      expect(getActiveChatPanel()?.hasPendingInput()).toBe(false)
      typed = true
      expect(getActiveChatPanel()?.hasPendingInput()).toBe(true)
    })
  })
})
