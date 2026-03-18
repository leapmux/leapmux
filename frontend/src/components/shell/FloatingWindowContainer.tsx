import type { Component, JSX } from 'solid-js'
import type { FloatingWindowStoreType } from '~/stores/floatingWindow.store'
import X from 'lucide-solid/icons/x'
import { IconButton } from '~/components/common/IconButton'
import * as styles from './FloatingWindowContainer.css'

interface FloatingWindowContainerProps {
  windowId: string
  x: number
  y: number
  width: number
  height: number
  zIndex: number
  title: string
  floatingWindowStore: FloatingWindowStoreType
  onClose: () => void
  children: JSX.Element
}

type ResizeDir = 'n' | 's' | 'e' | 'w' | 'ne' | 'nw' | 'se' | 'sw'

export const FloatingWindowContainer: Component<FloatingWindowContainerProps> = (props) => {
  let containerRef: HTMLDivElement | undefined

  const getContainerParent = () => containerRef?.parentElement

  const toFractional = (pxX: number, pxY: number) => {
    const parent = getContainerParent()
    if (!parent)
      return { fx: 0, fy: 0 }
    const rect = parent.getBoundingClientRect()
    return { fx: pxX / rect.width, fy: pxY / rect.height }
  }

  // --- Drag ---
  const handleDragStart = (e: PointerEvent) => {
    if ((e.target as HTMLElement).closest('button'))
      return
    e.preventDefault()
    props.floatingWindowStore.bringToFront(props.windowId)

    const startX = e.clientX
    const startY = e.clientY
    const startFx = props.x
    const startFy = props.y

    const handleMove = (me: PointerEvent) => {
      const dx = me.clientX - startX
      const dy = me.clientY - startY
      const { fx: dfx, fy: dfy } = toFractional(dx, dy)
      props.floatingWindowStore.updatePosition(props.windowId, startFx + dfx, startFy + dfy)
    }

    const handleUp = () => {
      document.removeEventListener('pointermove', handleMove)
      document.removeEventListener('pointerup', handleUp)
    }

    document.addEventListener('pointermove', handleMove)
    document.addEventListener('pointerup', handleUp)
  }

  // --- Resize ---
  const handleResizeStart = (dir: ResizeDir, e: PointerEvent) => {
    e.preventDefault()
    e.stopPropagation()
    props.floatingWindowStore.bringToFront(props.windowId)

    const startX = e.clientX
    const startY = e.clientY
    const startFx = props.x
    const startFy = props.y
    const startFw = props.width
    const startFh = props.height

    const parent = getContainerParent()
    const parentW = parent?.getBoundingClientRect().width ?? 1
    const parentH = parent?.getBoundingClientRect().height ?? 1

    // Minimum size in fractions (300px / 200px approximation)
    const minW = 300 / parentW
    const minH = 200 / parentH

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
        const proposedW = startFw - dfx
        if (proposedW >= minW) {
          newW = proposedW
          newX = startFx + dfx
        }
      }
      if (dir.includes('s')) {
        newH = Math.max(startFh + dfy, minH)
      }
      if (dir.includes('n')) {
        const proposedH = startFh - dfy
        if (proposedH >= minH) {
          newH = proposedH
          newY = startFy + dfy
        }
      }

      props.floatingWindowStore.updatePosition(props.windowId, newX, newY)
      props.floatingWindowStore.updateSize(props.windowId, newW, newH)
    }

    const handleUp = () => {
      document.removeEventListener('pointermove', handleMove)
      document.removeEventListener('pointerup', handleUp)
    }

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
      }}
      onMouseDown={() => props.floatingWindowStore.bringToFront(props.windowId)}
      data-testid="floating-window"
      data-window-id={props.windowId}
    >
      {/* Title bar (drag handle) */}
      <div
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
      <div class={styles.resizeN} onPointerDown={e => handleResizeStart('n', e)} />
      <div class={styles.resizeS} onPointerDown={e => handleResizeStart('s', e)} />
      <div class={styles.resizeE} onPointerDown={e => handleResizeStart('e', e)} />
      <div class={styles.resizeW} onPointerDown={e => handleResizeStart('w', e)} />
      <div class={styles.resizeNE} onPointerDown={e => handleResizeStart('ne', e)} />
      <div class={styles.resizeNW} onPointerDown={e => handleResizeStart('nw', e)} />
      <div class={styles.resizeSE} onPointerDown={e => handleResizeStart('se', e)} />
      <div class={styles.resizeSW} onPointerDown={e => handleResizeStart('sw', e)} />
    </div>
  )
}
