import { afterEach, describe, expect, it, vi } from 'vitest'
import { getFocusedChatSend, registerPanelSend, unregisterPanelSend } from './focusedChatSend.store'

afterEach(() => {
  document.body.innerHTML = ''
})

describe('focusedChatSend store', () => {
  it('returns the send fn for the panel containing the focused element', () => {
    const panel = document.createElement('div')
    panel.setAttribute('data-chat-panel', '')
    const input = document.createElement('input')
    panel.appendChild(input)
    document.body.appendChild(panel)

    const send = vi.fn()
    registerPanelSend(panel, send)
    input.focus()

    const resolved = getFocusedChatSend()
    expect(resolved).toBe(send)
  })

  it('returns undefined when focus is outside any chat panel', () => {
    const panel = document.createElement('div')
    panel.setAttribute('data-chat-panel', '')
    document.body.appendChild(panel)
    registerPanelSend(panel, vi.fn())

    const outside = document.createElement('input')
    document.body.appendChild(outside)
    outside.focus()

    expect(getFocusedChatSend()).toBeUndefined()
  })

  it('resolves the panel that actually contains the focused element when multiple are registered', () => {
    const panelA = document.createElement('div')
    panelA.setAttribute('data-chat-panel', '')
    const inputA = document.createElement('input')
    panelA.appendChild(inputA)

    const panelB = document.createElement('div')
    panelB.setAttribute('data-chat-panel', '')
    const inputB = document.createElement('input')
    panelB.appendChild(inputB)

    document.body.append(panelA, panelB)

    const sendA = vi.fn()
    const sendB = vi.fn()
    registerPanelSend(panelA, sendA)
    registerPanelSend(panelB, sendB)

    inputB.focus()
    expect(getFocusedChatSend()).toBe(sendB)

    inputA.focus()
    expect(getFocusedChatSend()).toBe(sendA)
  })

  it('returns undefined after unregister', () => {
    const panel = document.createElement('div')
    panel.setAttribute('data-chat-panel', '')
    const input = document.createElement('input')
    panel.appendChild(input)
    document.body.appendChild(panel)

    registerPanelSend(panel, vi.fn())
    input.focus()
    unregisterPanelSend(panel)

    expect(getFocusedChatSend()).toBeUndefined()
  })
})
