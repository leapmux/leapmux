/**
 * Keep every chat tile's scroll position across a heavy re-render elsewhere in
 * the app.
 *
 * It lives beside `ChatView`, which sets the `data-chat-scroll-container`
 * attribute this reads. The repair was written inside the title bar's
 * "Open in ..." button, then moved into the external-app hook when three more
 * menus started running the same refresh — a module about external
 * applications knew the chat's DOM contract for reasons no reader of that
 * module could guess.
 *
 * WHAT IT REPAIRS. When a list the app re-renders is large enough, the Solid
 * flush → DOM diff → browser layout pass takes long enough (~70 ms) to block
 * rAF, and during that gap the active chat tile's `scrollTop` is reset to 0 by
 * some browser-internal mechanism that extensive instrumentation did not
 * pinpoint. Snapshotting before and restoring after is a repair for the
 * symptom, not for the cause.
 */

/** Selector for the chat scroll containers `ChatView` marks. */
const CHAT_SCROLL_CONTAINER_SELECTOR = '[data-chat-scroll-container="true"]'

/**
 * The owner of one chat scroll container, as this module needs it. The module never
 * reads a snapshot: it hands each one back to the controller that made it.
 *
 * A raw `scrollTop` is no position to restore: the chat list moves its rows by a layout
 * shift, so the same `scrollTop` shows other rows after a shift changes. And a bare
 * `el.scrollTop = ...` reads to the controller as a move of the reader. Only the
 * controller knows what the reader saw, and whether the view moved since.
 */
export interface ChatScrollController<T> {
  /** What the reader sees now, or `undefined` when nothing can be restored (a hidden pane). */
  snapshot: () => T | undefined
  /** Put the view back at `snapshot`, when it moved since. */
  restore: (snapshot: T) => void
}

/**
 * The controller that owns each chat element, keyed by that element.
 *
 * A WeakMap because the key IS the element: an entry disappears with the node,
 * so there is nothing to unregister and a detached container cannot keep a
 * controller alive.
 */
const controllers = new WeakMap<HTMLElement, ChatScrollController<unknown>>()

/**
 * Tell this module which controller owns `el`.
 *
 * `useChatScroll.attachListRef` is the one caller, because it is the one place
 * that knows which controller owns which element. Pass `undefined` to forget
 * the element, which matters only for a controller that detaches while the
 * node stays.
 */
export function registerChatScrollController<T>(
  el: HTMLElement | undefined,
  controller: ChatScrollController<T> | undefined,
): void {
  if (!el)
    return
  if (controller)
    controllers.set(el, controller as ChatScrollController<unknown>)
  else
    controllers.delete(el)
}

interface ControllerSnapshot {
  el: HTMLElement
  controller: ChatScrollController<unknown>
  snapshot: unknown
}

function snapshotChatScroll(): ControllerSnapshot[] {
  // EVERY chat container, the visible tab plus any hidden one, so each
  // preserves its own state. `querySelector` alone would catch only the first
  // DOM match, which is often the hidden tab. A container that no controller
  // owns has nothing that can restore it.
  const snapshots: ControllerSnapshot[] = []
  for (const el of document.querySelectorAll<HTMLElement>(CHAT_SCROLL_CONTAINER_SELECTOR)) {
    const controller = controllers.get(el)
    if (controller === undefined)
      continue
    const snapshot = controller.snapshot()
    if (snapshot !== undefined)
      snapshots.push({ el, controller, snapshot })
  }
  return snapshots
}

function restoreChatScroll(snapshots: readonly ControllerSnapshot[]): void {
  // Run after Solid's flush and the browser's layout pass settle.
  requestAnimationFrame(() => requestAnimationFrame(() => {
    for (const { el, controller, snapshot } of snapshots) {
      if (el.isConnected)
        controller.restore(snapshot)
    }
  }))
}

/**
 * Run `work`, and put every chat tile's scroll position back afterwards.
 *
 * The snapshot is taken before `work` starts and the restore is scheduled once
 * it settles, whether it resolved or threw. `work` keeps its own rejection:
 * this wrapper repairs the DOM and decides nothing about the failure.
 */
export async function withChatScrollPreserved(work: () => Promise<void>): Promise<void> {
  const snapshots = snapshotChatScroll()
  try {
    await work()
  }
  finally {
    restoreChatScroll(snapshots)
  }
}
