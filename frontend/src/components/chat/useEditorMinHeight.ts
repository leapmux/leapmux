import type { Accessor } from 'solid-js'
import { createEffect, createSignal, on, onCleanup } from 'solid-js'
import { useWindowPointerDrag } from '~/components/shell/windowPointerDrag'
import {
  clampEditorHeight,
  clearEditorMinHeight,
  EDITOR_MIN_HEIGHT,
  getStoredEditorMinHeight,
  persistEditorMinHeight,
} from '~/lib/editor/editorMinHeight'

// In-memory cache of per-agent heights (avoids localStorage reads on every render).
const editorMinHeightCache = new Map<string, number | undefined>()

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
 * double-click reset, and persist to localStorage. The cache avoids localStorage
 * reads on every render when the user is rapidly switching agents.
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

  // Load per-agent height when agentId changes.
  createEffect(on(opts.agentId, (agentId) => {
    if (!agentId)
      return
    if (!editorMinHeightCache.has(agentId)) {
      editorMinHeightCache.set(agentId, getStoredEditorMinHeight(agentId))
    }
    setEditorMinHeightSignal(editorMinHeightCache.get(agentId))
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
