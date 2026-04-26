import type { Component, JSX } from 'solid-js'
import type { FloatingWindowStoreType } from '~/stores/floatingWindow.store'
import X from 'lucide-solid/icons/x'
import { For, onCleanup, onMount } from 'solid-js'
import { IconButton } from '~/components/common/IconButton'
import * as styles from './FloatingWindowContainer.css'

const SNAP_THRESHOLD_PX = 15

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

  // Track active pointer listeners for cleanup on unmount
  let activeMoveHandler: ((e: PointerEvent) => void) | null = null
  let activeUpHandler: (() => void) | null = null

  const cleanupPointerListeners = () => {
    if (activeMoveHandler) {
      document.removeEventListener('pointermove', activeMoveHandler)
      activeMoveHandler = null
    }
    if (activeUpHandler) {
      document.removeEventListener('pointerup', activeUpHandler)
      activeUpHandler = null
    }
  }

  onCleanup(cleanupPointerListeners)

  const getContainerParent = () => containerRef?.parentElement

  const toFractional = (pxX: number, pxY: number) => {
    const parent = getContainerParent()
    if (!parent)
      return { fx: 0, fy: 0 }
    const rect = parent.getBoundingClientRect()
    return { fx: pxX / rect.width, fy: pxY / rect.height }
  }

  // --- Edge snapping ---

  // --- Opacity (scroll on titlebar) ---
  const handleTitleBarWheel = (e: WheelEvent) => {
    e.preventDefault()
    const delta = e.deltaY > 0 ? -0.05 : 0.05
    const newOpacity = props.opacity + delta
    props.floatingWindowStore.updateOpacity(props.windowId, newOpacity)
    props.onGeometryChange?.()
  }

  onMount(() => {
    titleBarRef?.addEventListener('wheel', handleTitleBarWheel, { passive: false })
    onCleanup(() => {
      titleBarRef?.removeEventListener('wheel', handleTitleBarWheel)
    })
  })

  // --- Drag ---
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

    const parent = getContainerParent()
    const parentW = parent?.getBoundingClientRect().width ?? 1
    const parentH = parent?.getBoundingClientRect().height ?? 1

    const handleMove = (me: PointerEvent) => {
      const dx = me.clientX - startX
      const dy = me.clientY - startY
      const { fx: dfx, fy: dfy } = toFractional(dx, dy)
      const rawX = startFx + dfx
      const rawY = startFy + dfy
      const snapped = snapPosition(rawX, rawY, props.width, props.height, parentW, parentH)
      props.floatingWindowStore.updatePosition(props.windowId, snapped.x, snapped.y)
    }

    const handleUp = () => {
      cleanupPointerListeners()
      props.onGeometryChange?.()
    }

    cleanupPointerListeners()
    activeMoveHandler = handleMove
    activeUpHandler = handleUp
    document.addEventListener('pointermove', handleMove)
    document.addEventListener('pointerup', handleUp)
  }

  // --- Resize ---
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

    const parent = getContainerParent()
    const parentW = parent?.getBoundingClientRect().width ?? 1
    const parentH = parent?.getBoundingClientRect().height ?? 1

    const minW = 0.05
    const minH = 0.05

    const handleMove = (me: PointerEvent) => {
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

      props.floatingWindowStore.updatePosition(props.windowId, newX, newY)
      props.floatingWindowStore.updateSize(props.windowId, newW, newH)
    }

    const handleUp = () => {
      cleanupPointerListeners()
      props.onGeometryChange?.()
    }

    cleanupPointerListeners()
    activeMoveHandler = handleMove
    activeUpHandler = handleUp
    document.addEventListener('pointermove', handleMove)
    document.addEventListener('pointerup', handleUp)
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
        onPointerDown={handleDragStart}
      >
        <span class={styles.titleText}>{props.title}</span>
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
