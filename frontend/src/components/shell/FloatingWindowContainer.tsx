import type { Component, JSX } from 'solid-js'
import type { FloatingWindowStoreType } from '~/stores/floatingWindow.store'
import X from 'lucide-solid/icons/x'
import { For, onCleanup, onMount } from 'solid-js'
import { ClippedText } from '~/components/common/ClippedText'
import { IconButton } from '~/components/common/IconButton'
import { MIN_WINDOW_DIMENSION } from '~/stores/floatingWindow.store'
import * as styles from './FloatingWindowContainer.css'
import { useWindowPointerDrag } from './windowPointerDrag'

const SNAP_THRESHOLD_PX = 15

/**
 * Resolve the pixel-space dimensions of the nearest non-zero-sized,
 * attached ancestor of `el`. Exported (and pure) so unit tests can
 * cover the detached-parent fallback path that drag math relies on.
 *
 * Walk: start at `el?.parentElement`, climb until we find an ancestor
 * with a non-zero `getBoundingClientRect()`. If none of the ancestors
 * is valid (the element is fully detached from the document, which
 * happens transiently when `<For>` recreates the container on a CRDT
 * tick mid-pointerdown), fall back to the visual viewport.
 *
 * Without this fallback, `parentW`/`parentH` collapsed to `1`, the
 * drag handler treated pixel deltas as fractional values, and tiny
 * pointer movements snapped the window to corners.
 */
export function resolveParentSize(el: HTMLElement | null | undefined): { parentW: number, parentH: number } {
  let cur: HTMLElement | null = el?.parentElement ?? null
  while (cur) {
    const rect = cur.getBoundingClientRect()
    if (rect.width > 0 && rect.height > 0)
      return { parentW: rect.width, parentH: rect.height }
    cur = cur.parentElement
  }
  const fallbackW = (typeof window !== 'undefined' && (window.visualViewport?.width || window.innerWidth)) || 1
  const fallbackH = (typeof window !== 'undefined' && (window.visualViewport?.height || window.innerHeight)) || 1
  return { parentW: fallbackW, parentH: fallbackH }
}

/**
 * Snap a fractional (0..1) window position to the nearest parent edge if it
 * is within `SNAP_THRESHOLD_PX` of that edge. `x`/`y`/`w`/`h` are fractions of
 * `parentW`/`parentH`. Returns the (possibly snapped) `{x, y}`. Snapping is
 * performed independently along each axis: left vs. right, top vs. bottom.
 */
export function snapPosition(
  x: number,
  y: number,
  w: number,
  h: number,
  parentW: number,
  parentH: number,
): { x: number, y: number } {
  const snapX = SNAP_THRESHOLD_PX / parentW
  const snapY = SNAP_THRESHOLD_PX / parentH

  let snappedX = x
  let snappedY = y

  // Snap left edge
  if (Math.abs(x) < snapX)
    snappedX = 0
  // Snap right edge
  else if (Math.abs(x + w - 1) < snapX)
    snappedX = 1 - w

  // Snap top edge
  if (Math.abs(y) < snapY)
    snappedY = 0
  // Snap bottom edge
  else if (Math.abs(y + h - 1) < snapY)
    snappedY = 1 - h

  return { x: snappedX, y: snappedY }
}

interface FloatingWindowContainerProps {
  windowId: string
  x: number
  y: number
  width: number
  height: number
  opacity: number
  zIndex: number
  title: string
  floatingWindowStore: FloatingWindowStoreType
  onClose: () => void
  onActivate?: () => void
  onGeometryChange?: () => void
  children: JSX.Element
}

const RESIZE_HANDLES = [
  { dir: 'n', class: styles.resizeN },
  { dir: 's', class: styles.resizeS },
  { dir: 'e', class: styles.resizeE },
  { dir: 'w', class: styles.resizeW },
  { dir: 'ne', class: styles.resizeNE },
  { dir: 'nw', class: styles.resizeNW },
  { dir: 'se', class: styles.resizeSE },
  { dir: 'sw', class: styles.resizeSW },
] as const

type ResizeDir = typeof RESIZE_HANDLES[number]['dir']

export const FloatingWindowContainer: Component<FloatingWindowContainerProps> = (props) => {
  let containerRef: HTMLDivElement | undefined
  let titleBarRef: HTMLDivElement | undefined

  // Single document-level drag at a time — drag and resize share this
  // controller, so a fresh pointerdown on the title bar cancels any
  // in-flight resize and vice-versa. Cleanup on unmount is handled inside.
  const drag = useWindowPointerDrag()

  // Capture the parent's pixel dimensions once at drag-start.
  // Subsequent pointermove ticks divide by these so the per-event
  // path doesn't re-read `getBoundingClientRect` (which would force a
  // layout per frame). The defensive ancestor walk in
  // `resolveParentSize` matters when `<For>` has just torn down this
  // container on a CRDT tick: the pointerdown can land on a
  // momentarily-detached node whose `parentElement` is null.
  const captureParentSize = () => resolveParentSize(containerRef)

  // --- Edge snapping ---

  // --- Opacity (scroll on titlebar) ---
  // The wheel can fire dozens of times per scroll gesture on a free-spinning
  // trackpad. `updateOpacityDebounced` writes the live value to a local
  // override (instant feedback) and arms a trailing debounce that emits ONE
  // CRDT op when scrolling pauses — collapsing a whole scroll gesture to a
  // single op instead of one per wheel tick.
  const handleTitleBarWheel = (e: WheelEvent) => {
    e.preventDefault()
    if (e.deltaY === 0)
      return
    const delta = e.deltaY > 0 ? -0.05 : 0.05
    const changed = props.floatingWindowStore.updateOpacityDebounced(props.windowId, props.opacity + delta)
    if (changed)
      props.onGeometryChange?.()
  }

  onMount(() => {
    titleBarRef?.addEventListener('wheel', handleTitleBarWheel, { passive: false })
    onCleanup(() => {
      titleBarRef?.removeEventListener('wheel', handleTitleBarWheel)
    })
  })

  // Flush a pending debounced-opacity op when this container unmounts so a
  // scroll in flight when the window closes isn't lost.
  //
  // Also abort any drag still in flight for THIS window. `useWindowPointerDrag`
  // reacts to unmount by calling stop(), which deliberately fires no onUp, so
  // `commitDragGeometry` never runs and the local override would otherwise
  // outlive the gesture — the projection reads it unconditionally by id, so the
  // window would render frozen at never-committed geometry (and mask incoming
  // CRDT geometry) until some later drag replaced the override. Aborting rather
  // than committing is deliberate: an unmount is not a drop, so the partial
  // motion should not be persisted.
  onCleanup(() => {
    props.floatingWindowStore.flushOpacity(props.windowId)
    props.floatingWindowStore.cancelDragGeometry(props.windowId)
  })

  // --- Drag ---
  // The drag writes the live geometry into a LOCAL store override (no CRDT op)
  // so the window tracks the pointer responsively, then commits ONE op on drop
  // (commitDragGeometry). Peers see the window jump to its final position on
  // release instead of gliding frame-by-frame — the same tradeoff the splitter
  // drag (useTileDragResize) already makes. This cuts a per-drag op storm from
  // ~60-120 (one per rAF frame) down to 1, which in turn slashes op-log growth,
  // wire volume, and the BatchCommitted-echo projection churn the per-frame
  // path produced.
  //
  // Both gestures end the same way, so they share one callback rather than two
  // copies of it.
  const endDragGesture = () => {
    props.floatingWindowStore.commitDragGeometry(props.windowId)
    props.onGeometryChange?.()
  }

  const handleDragStart = (e: PointerEvent) => {
    if ((e.target as HTMLElement).closest('button'))
      return
    e.preventDefault()
    props.floatingWindowStore.bringToFront(props.windowId)
    props.onActivate?.()

    const startX = e.clientX
    const startY = e.clientY
    const startFx = props.x
    const startFy = props.y

    const { parentW, parentH } = captureParentSize()

    drag.start({
      coalesce: true,
      onMove: (me) => {
        const dfx = (me.clientX - startX) / parentW
        const dfy = (me.clientY - startY) / parentH
        const rawX = startFx + dfx
        const rawY = startFy + dfy
        // Snap against the window's LIVE size, not the pointer-down snapshot.
        // A move override deliberately does not mask width/height, so the store
        // keeps projecting a peer's mid-drag resize; snapping against a frozen
        // size would leave the edge test and the rendered window disagreeing by
        // exactly the size delta, and commitDragGeometry would then persist that
        // wrong x/y to every client. props.width/height are live getters off the
        // reconciled store, so reading them per frame is the whole fix.
        const snapped = snapPosition(rawX, rawY, props.width, props.height, parentW, parentH)
        props.floatingWindowStore.updateDragMove(props.windowId, snapped.x, snapped.y)
      },
      onUp: endDragGesture,
    })
  }

  // --- Resize ---
  // Same override-during-drag + commit-on-drop pattern as the move handler
  // (see handleDragStart). The resize writes the live geometry into the local
  // override each frame; commitDragGeometry emits the single op on drop.
  const handleResizeStart = (dir: ResizeDir, e: PointerEvent) => {
    e.preventDefault()
    e.stopPropagation()
    props.floatingWindowStore.bringToFront(props.windowId)
    props.onActivate?.()

    const startX = e.clientX
    const startY = e.clientY
    const startFx = props.x
    const startFy = props.y
    const startFw = props.width
    const startFh = props.height

    const { parentW, parentH } = captureParentSize()

    const minW = MIN_WINDOW_DIMENSION
    const minH = MIN_WINDOW_DIMENSION

    drag.start({
      coalesce: true,
      onMove: (me) => {
        const dxPx = me.clientX - startX
        const dyPx = me.clientY - startY
        const dfx = dxPx / parentW
        const dfy = dyPx / parentH

        let newX = startFx
        let newY = startFy
        let newW = startFw
        let newH = startFh

        if (dir.includes('e')) {
          newW = Math.max(startFw + dfx, minW)
        }
        if (dir.includes('w')) {
          newW = Math.max(startFw - dfx, minW)
          newX = startFx + startFw - newW
        }
        if (dir.includes('s')) {
          newH = Math.max(startFh + dfy, minH)
        }
        if (dir.includes('n')) {
          newH = Math.max(startFh - dfy, minH)
          newY = startFy + startFh - newH
        }

        props.floatingWindowStore.updateDragResize(props.windowId, newX, newY, newW, newH)
      },
      onUp: endDragGesture,
    })
  }

  return (
    <div
      ref={containerRef}
      class={styles.floatingWindow}
      style={{
        'left': `${props.x * 100}%`,
        'top': `${props.y * 100}%`,
        'width': `${props.width * 100}%`,
        'height': `${props.height * 100}%`,
        'z-index': props.zIndex,
        'opacity': props.opacity,
      }}
      onMouseDown={() => {
        props.floatingWindowStore.bringToFront(props.windowId)
        props.onActivate?.()
      }}
      data-testid="floating-window"
      data-window-id={props.windowId}
    >
      {/* Title bar (drag handle) */}
      <div
        ref={titleBarRef}
        class={styles.titleBar}
        data-testid="floating-window-titlebar"
        onPointerDown={handleDragStart}
      >
        <ClippedText text={props.title} class={styles.titleText} />
        <IconButton
          icon={X}
          size="sm"
          class={styles.titleCloseButton}
          onClick={(e) => {
            e.stopPropagation()
            props.onClose()
          }}
          data-testid="floating-window-close"
          title="Close window"
        />
      </div>

      {/* Content */}
      <div class={styles.windowContent}>
        {props.children}
      </div>

      {/* Resize handles */}
      <For each={RESIZE_HANDLES}>
        {h => <div class={h.class} onPointerDown={e => handleResizeStart(h.dir, e)} />}
      </For>
    </div>
  )
}
