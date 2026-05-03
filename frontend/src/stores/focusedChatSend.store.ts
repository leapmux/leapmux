/**
 * Per-panel registry of "send the current message" functions, used by the
 * `chat.sendMessage` shortcut to invoke the send function of the chat panel
 * that currently contains keyboard focus.
 *
 * The registry is keyed by the panel's root DOM element rather than agent ID
 * so the focused-panel lookup falls out of `document.activeElement.closest()`
 * without leaking an agent ID into the DOM. The `WeakMap` lets entries be
 * collected automatically when the panel element is detached.
 */

const sendByPanel = new WeakMap<Element, () => void>()

export function registerPanelSend(panel: Element, send: () => void): void {
  sendByPanel.set(panel, send)
}

export function unregisterPanelSend(panel: Element): void {
  sendByPanel.delete(panel)
}

/** Resolve the send function for the panel containing `document.activeElement`. */
export function getFocusedChatSend(): (() => void) | undefined {
  const panel = document.activeElement?.closest('[data-chat-panel]')
  return panel ? sendByPanel.get(panel) : undefined
}
