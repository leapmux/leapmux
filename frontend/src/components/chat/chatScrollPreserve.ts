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

/** How a chat controller writes a scroll position it made itself. */
export type ProgrammaticScrollWriter = (top: number, source?: string) => void

/**
 * The controller that owns each chat element, keyed by that element.
 *
 * A restore is NOT a user gesture, and `useChatScroll` can only know that if
 * the write goes through its own programmatic path: that path records the
 * landing pixel so the resulting `scroll` event is recognized as the app's own,
 * syncs the velocity tracker, and advances the direction baseline. A bare
 * `el.scrollTop = ...` skips all three, so the return jump was measured as a
 * user fling -- which can defer a re-pin and can start an edge-pagination
 * fetch nobody asked for.
 *
 * A WeakMap because the key IS the element: an entry disappears with the node,
 * so there is nothing to unregister and a detached container cannot keep a
 * controller alive.
 */
const programmaticWriters = new WeakMap<HTMLElement, ProgrammaticScrollWriter>()

/**
 * Tell this module how to write `el`'s scroll position programmatically.
 *
 * `useChatScroll.attachListRef` is the one caller, because it is the one place
 * that knows which controller owns which element. Pass `undefined` to forget
 * the element, which matters only for a controller that detaches while the
 * node stays.
 */
export function registerProgrammaticScrollWriter(
  el: HTMLElement | undefined,
  write: ProgrammaticScrollWriter | undefined,
): void {
  if (!el)
    return
  if (write)
    programmaticWriters.set(el, write)
  else
    programmaticWriters.delete(el)
}

// Skip the restore if the chat's scrollHeight changed by more than this many
// pixels — that signals a real content reload, not the spurious clamp we are
// trying to undo.
const CHAT_SCROLL_RESTORE_HEIGHT_TOLERANCE_PX = 200

interface ScrollSnapshot {
  el: HTMLDivElement
  scrollTop: number
  scrollHeight: number
}

function snapshotChatScroll(): ScrollSnapshot[] {
  // EVERY chat container, the visible tab plus any hidden one, so each
  // preserves its own state. `querySelector` alone would catch only the first
  // DOM match, which is often the hidden tab.
  return Array.from(
    document.querySelectorAll<HTMLDivElement>(CHAT_SCROLL_CONTAINER_SELECTOR),
  ).map(el => ({ el, scrollTop: el.scrollTop, scrollHeight: el.scrollHeight }))
}

function restoreChatScroll(snapshots: readonly ScrollSnapshot[]): void {
  // Run after Solid's flush and the browser's layout pass settle.
  requestAnimationFrame(() => requestAnimationFrame(() => {
    for (const s of snapshots) {
      if (!s.el.isConnected)
        continue
      if (s.el.scrollTop === s.scrollTop)
        continue
      const heightDelta = Math.abs(s.el.scrollHeight - s.scrollHeight)
      if (heightDelta >= CHAT_SCROLL_RESTORE_HEIGHT_TOLERANCE_PX)
        continue
      // Through the controller when one owns this element, so the write is
      // marked as the app's own. The direct assignment is the fallback for an
      // element no controller registered, where an unmarked write still beats
      // leaving the position clamped to 0.
      const write = programmaticWriters.get(s.el)
      if (write)
        write(s.scrollTop, 'external-app-refresh')
      else
        s.el.scrollTop = s.scrollTop
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
