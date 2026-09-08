/**
 * Registry of what the keyboard layer may ask of a mounted chat panel.
 *
 * Two questions have two different owners, which is why there are two lookups.
 * `chat.sendMessage` acts on the panel that HOLDS FOCUS, so it resolves through
 * `document.activeElement.closest()` -- keyed by the panel's root element rather
 * than by agent id so the lookup falls out of the DOM without leaking an agent
 * id into it, and so a `WeakMap` collects the entry when the element detaches.
 * `chat.steerQueuedInput` acts on the CURRENT agent tab whether or not focus is
 * in the composer, so it reads the mounted panel directly.
 *
 * There is at most one mounted panel: `FocusedAgentEditorPanel` is rendered in
 * exactly one of the two shell layers (mobile and desktop are mutually
 * exclusive) and is keyed to the focused agent. `activePanel` therefore identifies
 * "the current agent tab's composer" unambiguously, and a re-registration
 * replaces it.
 */

/** What the keyboard layer can do with, or ask about, one chat panel. */
export interface FocusedChatPanel {
  /**
   * Submit the composer.
   *
   * Asynchronous: it awaits the enqueue RPC. Every caller discards the promise
   * deliberately, so the type states that instead of hiding it behind
   * `() => void`, which turns a rejection into an unhandled one at a call site
   * that looks synchronous.
   */
  send: () => void | Promise<void>
  /**
   * Whether submitting right now would send anything: text in the document, an
   * attachment, or a control request an empty submit would answer.
   *
   * An ACCESSOR, read at dispatch time, and never a snapshot boolean. The
   * panel's own `hasContent` signal is fed by a listener debounced 200 ms, so a
   * value captured at registration is wrong for the first fifth of a second
   * after every keystroke -- which is exactly when the user presses the send
   * chord. Reading through this function is what lets the caller consult the
   * live ProseMirror document instead.
   */
  hasPendingInput: () => boolean
}

const panelHandles = new WeakMap<Element, FocusedChatPanel>()
let activePanel: Element | undefined

export function registerChatPanel(panel: Element, handle: FocusedChatPanel): void {
  panelHandles.set(panel, handle)
  activePanel = panel
}

export function unregisterChatPanel(panel: Element): void {
  panelHandles.delete(panel)
  if (activePanel === panel)
    activePanel = undefined
}

/** The panel containing `document.activeElement`, if focus is inside one. */
export function getFocusedChatPanel(): FocusedChatPanel | undefined {
  const panel = document.activeElement?.closest('[data-chat-panel]')
  return panel ? panelHandles.get(panel) : undefined
}

/**
 * The mounted panel, whatever holds focus. This is the current agent tab's
 * composer -- see the module header for why exactly one can be mounted.
 */
export function getActiveChatPanel(): FocusedChatPanel | undefined {
  return activePanel ? panelHandles.get(activePanel) : undefined
}
