import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import { FloatingWindowContainer, resolveParentSize, snapPosition } from './FloatingWindowContainer'

describe('snapPosition', () => {
  // Use a 1000px parent so the 15px snap threshold equals exactly 0.015 fractional.
  const PARENT_W = 1000
  const PARENT_H = 1000
  const W = 0.4
  const H = 0.3

  it('does not snap when the window is far from any edge', () => {
    const result = snapPosition(0.3, 0.3, W, H, PARENT_W, PARENT_H)
    expect(result.x).toBe(0.3)
    expect(result.y).toBe(0.3)
  })

  it('snaps the left edge to 0 when within threshold', () => {
    const result = snapPosition(0.01, 0.5, W, H, PARENT_W, PARENT_H)
    expect(result.x).toBe(0)
    expect(result.y).toBe(0.5)
  })

  it('snaps the right edge to 1 - w when within threshold', () => {
    // Window's right edge at x + w; 1 - 0.01 = 0.99 → right edge near 1
    const result = snapPosition(0.59, 0.5, W, H, PARENT_W, PARENT_H)
    expect(result.x).toBe(1 - W)
  })

  it('snaps the top edge to 0 when within threshold', () => {
    const result = snapPosition(0.5, 0.01, W, H, PARENT_W, PARENT_H)
    expect(result.y).toBe(0)
  })

  it('snaps the bottom edge to 1 - h when within threshold', () => {
    const result = snapPosition(0.5, 0.69, W, H, PARENT_W, PARENT_H)
    expect(result.y).toBe(1 - H)
  })

  it('snaps both axes independently when both edges are within threshold', () => {
    const result = snapPosition(0.005, 0.005, W, H, PARENT_W, PARENT_H)
    expect(result.x).toBe(0)
    expect(result.y).toBe(0)
  })

  it('does not snap if the window is just past the threshold on the left', () => {
    // 0.02 > 0.015 threshold (15/1000), so no snap
    const result = snapPosition(0.02, 0.5, W, H, PARENT_W, PARENT_H)
    expect(result.x).toBe(0.02)
  })

  it('uses parent-relative thresholds', () => {
    // With a 100px parent, the 15px threshold is 0.15 fractional, so 0.1 should snap.
    const result = snapPosition(0.1, 0.5, W, H, 100, 100)
    expect(result.x).toBe(0)
  })
})

interface ContainerOpts {
  x?: number
  y?: number
  width?: number
  height?: number
  opacity?: number
  zIndex?: number
  title?: string
  onClose?: () => void
  onActivate?: () => void
}

function renderContainer(opts: ContainerOpts = {}) {
  const store = createFloatingWindowStore()
  const created = store.addWindow({
    x: opts.x ?? 0.1,
    y: opts.y ?? 0.1,
    width: opts.width ?? 0.4,
    height: opts.height ?? 0.3,
  })
  if (!created)
    throw new Error('addWindow returned null — vitest setup should wire a default CRDT bridge')
  const { windowId } = created
  return {
    store,
    windowId,
    ...render(() => (
      <FloatingWindowContainer
        windowId={windowId}
        x={opts.x ?? 0.1}
        y={opts.y ?? 0.1}
        width={opts.width ?? 0.4}
        height={opts.height ?? 0.3}
        opacity={opts.opacity ?? 1}
        zIndex={opts.zIndex ?? 100}
        title={opts.title ?? 'Test Window'}
        floatingWindowStore={store}
        onClose={opts.onClose ?? (() => {})}
        onActivate={opts.onActivate}
      >
        <div data-testid="window-content">child</div>
      </FloatingWindowContainer>
    )),
  }
}

// Regression: when the floating-window container is mid-remount (Solid's
// `<For>` recreates it on every CRDT tick because the projection used
// to produce fresh refs), a pointerdown landing on the title bar reads
// `containerRef.parentElement === null` and `resolveParentSize` used to
// fall back to `1 × 1`. The drag-math then treated pixel deltas as
// fractional values, snapping the window to corners on tiny drags.
// `resolveParentSize` now walks ancestors and falls back to the visual
// viewport so deltas remain pixel-accurate even mid-remount.
describe('resolveParentSize', () => {
  it('returns the immediate parent rect when it has non-zero dimensions', () => {
    const parent = document.createElement('div')
    Object.defineProperty(parent, 'getBoundingClientRect', {
      value: () => ({ width: 800, height: 600, top: 0, left: 0, right: 800, bottom: 600, x: 0, y: 0, toJSON: () => ({}) }),
    })
    const child = document.createElement('div')
    parent.appendChild(child)
    expect(resolveParentSize(child)).toEqual({ parentW: 800, parentH: 600 })
  })

  it('walks up the ancestor chain past zero-sized intermediates', () => {
    const grandparent = document.createElement('div')
    Object.defineProperty(grandparent, 'getBoundingClientRect', {
      value: () => ({ width: 1024, height: 768, top: 0, left: 0, right: 1024, bottom: 768, x: 0, y: 0, toJSON: () => ({}) }),
    })
    const parent = document.createElement('div')
    Object.defineProperty(parent, 'getBoundingClientRect', {
      value: () => ({ width: 0, height: 0, top: 0, left: 0, right: 0, bottom: 0, x: 0, y: 0, toJSON: () => ({}) }),
    })
    grandparent.appendChild(parent)
    const child = document.createElement('div')
    parent.appendChild(child)
    expect(resolveParentSize(child)).toEqual({ parentW: 1024, parentH: 768 })
  })

  it('falls back to the visual viewport when the element is detached', () => {
    // Detached element: parentElement is null. The drag-math regression
    // happens exactly here — `<For>` momentarily detaches the container
    // on a CRDT-state tick while a pointerdown is being dispatched.
    const detached = document.createElement('div')
    const result = resolveParentSize(detached)
    expect(result.parentW).toBeGreaterThan(1)
    expect(result.parentH).toBeGreaterThan(1)
  })

  it('falls back to the visual viewport when every ancestor is zero-sized', () => {
    const root = document.createElement('div')
    Object.defineProperty(root, 'getBoundingClientRect', {
      value: () => ({ width: 0, height: 0, top: 0, left: 0, right: 0, bottom: 0, x: 0, y: 0, toJSON: () => ({}) }),
    })
    const child = document.createElement('div')
    root.appendChild(child)
    const result = resolveParentSize(child)
    expect(result.parentW).toBeGreaterThan(1)
    expect(result.parentH).toBeGreaterThan(1)
  })

  it('returns viewport size when the input is null/undefined', () => {
    const a = resolveParentSize(null)
    const b = resolveParentSize(undefined)
    expect(a.parentW).toBeGreaterThan(1)
    expect(a.parentH).toBeGreaterThan(1)
    expect(b).toEqual(a)
  })
})

describe('floatingWindowContainer', () => {
  it('renders the window with title, content and close button', () => {
    renderContainer({ title: 'My Window' })
    expect(screen.getByTestId('floating-window')).toBeInTheDocument()
    expect(screen.getByText('My Window')).toBeInTheDocument()
    expect(screen.getByTestId('window-content')).toBeInTheDocument()
    expect(screen.getByTestId('floating-window-close')).toBeInTheDocument()
  })

  it('applies fractional position and size via inline style as percent', () => {
    renderContainer({ x: 0.25, y: 0.5, width: 0.4, height: 0.3 })
    const win = screen.getByTestId('floating-window')
    expect(win.style.left).toBe('25%')
    expect(win.style.top).toBe('50%')
    expect(win.style.width).toBe('40%')
    expect(win.style.height).toBe('30%')
  })

  it('applies opacity and zIndex from props', () => {
    renderContainer({ opacity: 0.5, zIndex: 42 })
    const win = screen.getByTestId('floating-window')
    expect(win.style.opacity).toBe('0.5')
    expect(win.style.zIndex).toBe('42')
  })

  it('exposes the window ID via data-window-id', () => {
    const { windowId } = renderContainer()
    const win = screen.getByTestId('floating-window')
    expect(win.getAttribute('data-window-id')).toBe(windowId)
  })

  it('invokes onClose when the close button is clicked', () => {
    const onClose = vi.fn()
    renderContainer({ onClose })
    fireEvent.click(screen.getByTestId('floating-window-close'))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('clicking the close button does not bubble to the window mousedown handler', () => {
    // Window mousedown calls bringToFront + onActivate; close button stops propagation.
    const onActivate = vi.fn()
    renderContainer({ onActivate })
    fireEvent.click(screen.getByTestId('floating-window-close'))
    // We only assert onActivate isn't called *by clicking the close button*.
    // The onMouseDown lives on the window itself, but click goes through
    // pointerdown → onMouseDown unless stopPropagation. We assert stopPropagation
    // by making sure activation didn't happen due to the close click.
    expect(onActivate).not.toHaveBeenCalled()
  })

  it('mousedown on the window invokes bringToFront and onActivate', () => {
    const onActivate = vi.fn()
    const { store, windowId } = renderContainer({ onActivate })
    // Add a second window so the rendered one is no longer topmost — without
    // this, bringToFront short-circuits as a no-op (see floatingWindow.store).
    store.addWindow()
    const win = screen.getByTestId('floating-window')
    // The rendered window was added first, so it's at index 0; the second
    // (topmost) window sits at index 1.
    expect(store.state.windows.findIndex(w => w.id === windowId)).toBe(0)
    fireEvent.mouseDown(win)
    expect(onActivate).toHaveBeenCalledTimes(1)
    // After bringToFront the rendered window must have moved to the end.
    expect(store.state.windows.findIndex(w => w.id === windowId)).toBe(1)
  })
})
