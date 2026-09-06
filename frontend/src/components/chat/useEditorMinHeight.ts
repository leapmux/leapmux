import type { Accessor } from 'solid-js'
import { createEffect, createSignal, on, onCleanup } from 'solid-js'
import { useWindowPointerDrag } from '~/components/shell/windowPointerDrag'
import { onStorageAccountChange } from '~/lib/browserStorage'
import {
  clampEditorHeight,
  clearEditorMinHeight,
  EDITOR_MIN_HEIGHT,
  getStoredEditorMinHeight,
  persistEditorMinHeight,
} from '~/lib/editor/editorMinHeight'

// In-memory cache of per-agent heights. The stored height is on the
// ASYNCHRONOUS storage tier, which a render cannot await, so this cache is what
// answers the render path after the first read lands.
//
// It MIRRORS an account-scoped key (`PREFIX_EDITOR_MIN_HEIGHT` is
// `scope: 'account'`), so it follows the namespace, which is the rule
// `~/lib/browserStorage` states for exactly this shape. An in-tab account switch
// needs no reload -- `AuthContext` moves the namespace in place -- so without
// this the next account reads the previous one's entries. It also bounds the
// map, which otherwise keeps one entry per agent for the page's life.
const editorMinHeightCache = new Map<string, number | undefined>()

// Through a NAMED function, so `resetEditorMinHeightCacheForTests` can register
// it again.
function dropCachedHeights(): void {
  editorMinHeightCache.clear()
}

onStorageAccountChange(dropCachedHeights)

/**
 * Drop the cache and subscribe to the account move again. FOR TESTS ONLY.
 *
 * `resetStorageAccountForTests` CLEARS every account listener, and the suite
 * runs it before each test -- so this module, which subscribes at import time,
 * is unsubscribed for the whole file and its account-switch behaviour
 * disappears from every test with no failure to show for it. Registering again
 * here is what makes that behaviour reachable at all. The listeners live in a
 * Set keyed by reference, so the second registration is a no-op in production,
 * where nothing calls this. `~/lib/accountScopedSignal` carries the same seam
 * for the same reason.
 */
export function resetEditorMinHeightCacheForTests(): void {
  editorMinHeightCache.clear()
  onStorageAccountChange(dropCachedHeights)
}

export interface UseEditorMinHeightOptions {
  /** Agent ID used as the storage key. */
  agentId: Accessor<string | undefined>
  /** Height of the parent container, used for max editor height calculation. */
  containerHeight: Accessor<number | undefined>
  /** Returns the panel root element so the resize handler can query the editor wrapper for its current height. */
  panelRef: Accessor<HTMLDivElement | undefined>
}

export interface UseEditorMinHeightResult {
  editorMinHeight: Accessor<number | undefined>
  isDragging: Accessor<boolean>
  maxEditorHeight: () => number
  handleResizeStart: (e: PointerEvent) => void
  resetEditorHeight: () => void
}

/**
 * Manages the per-agent editor minimum height: load on agent change, drag-to-resize,
 * double-click reset, and persist. The stored height is on the ASYNCHRONOUS
 * tier, so the cache is what answers a render while a read is still in flight,
 * and what keeps a rapid agent switch from issuing a read per frame.
 */
export function useEditorMinHeight(opts: UseEditorMinHeightOptions): UseEditorMinHeightResult {
  const [isDragging, setIsDragging] = createSignal(false)
  const [editorMinHeightSignal, setEditorMinHeightSignal] = createSignal<number | undefined>(undefined)
  // Single-controller drag: auto-detaches document move listeners on
  // component unmount so a drag in flight at unmount time can't leak
  // listeners. The pointerup cleanup listener is tracked separately below.
  const drag = useWindowPointerDrag()
  // Active pointerup/pointercancel cleanup for the in-flight drag, if any.
  // Tracked at hook scope so unmount mid-drag detaches it explicitly — the
  // `useWindowPointerDrag` helper only owns its own pointermove listener.
  let detachFinish: (() => void) | null = null
  onCleanup(() => detachFinish?.())

  // Load the per-agent height when agentId changes.
  //
  // The cache is what keeps this synchronous in the common case: an agent seen
  // before answers immediately, so switching back to it does not flash the
  // default height. Only the FIRST visit to an agent pays a read, and the guard
  // on `opts.agentId()` after it stops a slow read from applying an older
  // agent's height to the one now on screen.
  //
  // A MISS PUBLISHES NOTHING until the read lands. Writing `undefined` first
  // would collapse the composer to EDITOR_MIN_HEIGHT and expand it again one
  // round trip later, for every agent the session has not visited yet -- the
  // synchronous read this replaced set the height once, before paint. Holding
  // the current value costs at most one frame of the outgoing agent's height,
  // and on a cold start that value is already `undefined`, which is the right
  // answer for an agent with no stored override.
  createEffect(on(opts.agentId, (agentId) => {
    if (!agentId)
      return
    if (editorMinHeightCache.has(agentId)) {
      setEditorMinHeightSignal(editorMinHeightCache.get(agentId))
      return
    }
    void getStoredEditorMinHeight(agentId).then((stored) => {
      // A write may have landed for this agent while the read was in flight
      // (a resize drag ends in `setEditorMinHeight` below), and it is newer
      // than what the disk held.
      if (!editorMinHeightCache.has(agentId))
        editorMinHeightCache.set(agentId, stored)
      if (opts.agentId() === agentId)
        setEditorMinHeightSignal(editorMinHeightCache.get(agentId))
    })
  }))

  const setEditorMinHeight = (val: number | undefined) => {
    setEditorMinHeightSignal(val)
    const id = opts.agentId()
    if (id)
      editorMinHeightCache.set(id, val)
  }

  const maxEditorHeight = () => {
    const h = opts.containerHeight() ?? 0
    return h > 0 ? Math.floor(h * 0.5) : 200
  }

  const handleResizeStart = (e: PointerEvent) => {
    e.preventDefault()
    setIsDragging(true)
    const startY = e.clientY
    const maxHeight = maxEditorHeight()
    // Use the current visual height of the editor wrapper as the drag starting
    // point so the drag feels anchored to the handle's visual position.
    const panel = opts.panelRef()
    const editorWrapperEl = panel?.querySelector('[data-testid="chat-editor"]') as HTMLElement | null
    const startHeight = editorWrapperEl?.getBoundingClientRect().height
      ?? editorMinHeightSignal()
      ?? EDITOR_MIN_HEIGHT
    document.body.style.cursor = 'row-resize'

    // The helper handles move dispatching + auto-cleanup on unmount, but
    // its `onUp` is suppressed on bare clicks (no move). The original
    // behavior persisted unconditionally on mouseup, so we run the
    // cleanup/persist from a paired pointerup listener instead of the
    // helper's `onUp`.
    drag.start({
      onMove: (moveEvent) => {
        const delta = startY - moveEvent.clientY
        setEditorMinHeight(clampEditorHeight(startHeight + delta, maxHeight))
      },
    })
    detachFinish?.()
    const finish = () => {
      detachFinish = null
      document.removeEventListener('pointerup', finish)
      document.removeEventListener('pointercancel', finish)
      setIsDragging(false)
      document.body.style.cursor = ''
      const id = opts.agentId()
      if (id)
        persistEditorMinHeight(id, editorMinHeightSignal())
    }
    detachFinish = () => {
      document.removeEventListener('pointerup', finish)
      document.removeEventListener('pointercancel', finish)
    }
    document.addEventListener('pointerup', finish, { once: true })
    document.addEventListener('pointercancel', finish, { once: true })
  }

  const resetEditorHeight = () => {
    setEditorMinHeightSignal(undefined)
    const id = opts.agentId()
    if (id) {
      editorMinHeightCache.set(id, undefined)
      clearEditorMinHeight(id)
    }
  }

  return {
    editorMinHeight: editorMinHeightSignal,
    isDragging,
    maxEditorHeight,
    handleResizeStart,
    resetEditorHeight,
  }
}
