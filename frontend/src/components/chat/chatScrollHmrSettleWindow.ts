import { onCleanup, onMount } from 'solid-js'

// ---------------------------------------------------------------------------
// The HMR settle window of the chat list (dev server only)
//
// A hot reload re-renders the WHOLE message list at once, so every row measures again in a
// burst, and the total height changes for about a second. A pre-fetch of the buffer filler
// on top of that burst makes the view drift on its own after an HMR. So the buffer fill of a
// mount that an HMR module swap caused waits until the geometry settles, or until the
// developer scrolls.
//
// Only a mount after a real HMR module swap opens the window. The scroll hook
// (useChatScroll.ts) detects that swap, because it marks the swap of its own module, and its
// `hmrRemount` states why no other mount may open the window.
// ---------------------------------------------------------------------------

/** How long after an HMR remount the buffer fill stays paused, unless the window ends sooner. */
export const HMR_SETTLE_MS = 600

export interface HmrSettleWindow {
  /** True from the mount of an HMR remount until HMR_SETTLE_MS pass or `end` runs. */
  isSettling: () => boolean
  /** End the window now: the reader moved the view or gave a command. */
  end: () => void
}

/**
 * Create the settle window of one mount. `hmrRemount` is true for a mount in the burst of
 * remounts that follows an HMR module swap, and false for every other mount: a cold load,
 * production, and the tests. Call it inside a reactive owner: the timer starts at the
 * mount, and the cleanup of the owner clears it.
 */
export function createHmrSettleWindow(hmrRemount: boolean): HmrSettleWindow {
  let settling = hmrRemount
  onMount(() => {
    if (!settling || typeof setTimeout !== 'function')
      return
    const timer = setTimeout(() => {
      settling = false
    }, HMR_SETTLE_MS)
    onCleanup(() => clearTimeout(timer))
  })
  return {
    isSettling: () => settling,
    end: () => {
      settling = false
    },
  }
}
