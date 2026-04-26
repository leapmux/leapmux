import type { Accessor } from 'solid-js'
import { createEffect, createSignal, on, onCleanup } from 'solid-js'
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
  handleResizeStart: (e: MouseEvent) => void
  handleResizeReset: () => void
  /** Same as handleResizeReset but exposed for non-UI callers (e.g. control responses). */
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
  // Holds the in-flight drag teardown so component unmount mid-drag still
  // detaches the document-level mousemove/mouseup listeners.
  let activeDragCleanup: (() => void) | null = null
  onCleanup(() => activeDragCleanup?.())

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

  const handleResizeStart = (e: MouseEvent) => {
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

    const onMouseMove = (moveEvent: MouseEvent) => {
      const delta = startY - moveEvent.clientY
      setEditorMinHeight(clampEditorHeight(startHeight + delta, maxHeight))
    }

    const onMouseUp = () => {
      setIsDragging(false)
      activeDragCleanup?.()
      const id = opts.agentId()
      if (id)
        persistEditorMinHeight(id, editorMinHeightSignal())
    }

    document.addEventListener('mousemove', onMouseMove)
    document.addEventListener('mouseup', onMouseUp)
    activeDragCleanup = () => {
      document.body.style.cursor = ''
      document.removeEventListener('mousemove', onMouseMove)
      document.removeEventListener('mouseup', onMouseUp)
      activeDragCleanup = null
    }
  }

  const handleResizeReset = () => {
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
    handleResizeReset,
    resetEditorHeight: handleResizeReset,
  }
}
