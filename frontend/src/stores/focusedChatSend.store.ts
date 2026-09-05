/**
 * Per-panel registry of "send the current message" functions, used by the
 * `chat.sendMessage` shortcut to invoke the send function of the chat panel
 * that currently contains keyboard focus.
 *
 * The registry is keyed by the panel's root DOM element rather than agent ID
 * so the focused-panel lookup falls out of `document.activeElement.closest()`
 * without leaking an agent ID into the DOM. The `WeakMap` lets entries be
 * collected automatically when the panel element is detached.
 *
 * The send function is asynchronous: it awaits the enqueue RPC. Every caller
 * discards the promise deliberately, so the type states that instead of
 * hiding it behind `() => void`, which turns a rejection into an unhandled
 * one at a call site that looks synchronous.
 */

type PanelSend = () => void | Promise<void>

const sendByPanel = new WeakMap<Element, PanelSend>()

export function registerPanelSend(panel: Element, send: PanelSend): void {
  sendByPanel.set(panel, send)
}

export function unregisterPanelSend(panel: Element): void {
  sendByPanel.delete(panel)
}

/** Resolve the send function for the panel containing `document.activeElement`. */
export function getFocusedChatSend(): PanelSend | undefined {
  const panel = document.activeElement?.closest('[data-chat-panel]')
  return panel ? sendByPanel.get(panel) : undefined
}
