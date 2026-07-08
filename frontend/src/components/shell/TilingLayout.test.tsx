import type { LayoutNodeLocal } from '~/stores/layout.store'
import { render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { TilingLayout } from '~/components/shell/TilingLayout'
import {
  dispatchPointerDown,
  dispatchPointerMove,
  dispatchPointerUp,
  installPointerEventShim,
  stubBoundingRect,
} from '~/test-support/pointer'

beforeAll(installPointerEventShim)

afterEach(() => {
  document.body.innerHTML = ''
})

const renderTile = (id: string) => (<div data-testid={`tile-${id}`}>{id}</div>)

describe('tilingLayout', () => {
  describe('split renderer', () => {
    it('emits live-preview ratios via inline grid-template-columns and commits once on pointerup', () => {
      const root: LayoutNodeLocal = {
        type: 'split',
        id: 'sp',
        direction: 'vertical',
        ratios: [0.5, 0.5],
        children: [
          { type: 'leaf', id: 'a' },
          { type: 'leaf', id: 'b' },
        ],
      }
      const onRatioChange = vi.fn()
      const { container } = render(() => (
        <TilingLayout root={root} renderTile={renderTile} onRatioChange={onRatioChange} />
      ))
      const split = container.querySelector('[data-testid="tile-split"]') as HTMLElement
      stubBoundingRect(split, 1000, 600)

      const handle = container.querySelector('[data-testid="tile-resize-handle"]') as HTMLElement
      expect(handle).not.toBeNull()
      dispatchPointerDown(handle, { x: 0 })
      dispatchPointerMove({ x: 200 })

      // Live preview reflects in inline style.
      expect(split.style.getPropertyValue('grid-template-columns')).toMatch(/0\.7.*?fr.*?0\.3.*?fr/)
      expect(onRatioChange).not.toHaveBeenCalled()

      dispatchPointerUp({ x: 200 })
      expect(onRatioChange).toHaveBeenCalledTimes(1)
      expect(onRatioChange.mock.calls[0]?.[0]).toBe('sp')
      const finalRatios = onRatioChange.mock.calls[0]?.[1] as number[]
      expect(finalRatios[0]).toBeCloseTo(0.7, 9)
      expect(finalRatios[1]).toBeCloseTo(0.3, 9)
    })

    it('renders separator with role="separator" and aria-orientation="vertical" for vertical-divider splits', () => {
      const root: LayoutNodeLocal = {
        type: 'split',
        id: 'sp',
        direction: 'vertical',
        ratios: [0.5, 0.5],
        children: [
          { type: 'leaf', id: 'a' },
          { type: 'leaf', id: 'b' },
        ],
      }
      const { container } = render(() => (
        <TilingLayout root={root} renderTile={renderTile} />
      ))
      const handle = container.querySelector('[data-testid="tile-resize-handle"]') as HTMLElement
      expect(handle.getAttribute('role')).toBe('separator')
      expect(handle.getAttribute('aria-orientation')).toBe('vertical')
    })

    it('cancels in-flight drag without committing when ratios mutate externally', () => {
      const [layout, setLayout] = createSignal<LayoutNodeLocal>({
        type: 'split',
        id: 'sp',
        direction: 'vertical',
        ratios: [0.5, 0.5],
        children: [
          { type: 'leaf', id: 'a' },
          { type: 'leaf', id: 'b' },
        ],
      })
      const onRatioChange = vi.fn()
      const { container } = render(() => (
        <TilingLayout root={layout()} renderTile={renderTile} onRatioChange={onRatioChange} />
      ))
      const split = container.querySelector('[data-testid="tile-split"]') as HTMLElement
      stubBoundingRect(split, 1000, 600)

      const handle = container.querySelector('[data-testid="tile-resize-handle"]') as HTMLElement
      dispatchPointerDown(handle, { x: 0 })
      dispatchPointerMove({ x: 100 })

      // Mutate ratios externally — the structural-cancel effect should
      // fire and abort the in-flight drag.
      setLayout({
        type: 'split',
        id: 'sp',
        direction: 'vertical',
        ratios: [0.4, 0.6],
        children: [
          { type: 'leaf', id: 'a' },
          { type: 'leaf', id: 'b' },
        ],
      })

      // A subsequent pointerup must not commit (the drag was already
      // torn down; helper finalize is idempotent).
      dispatchPointerUp({ x: 100 })
      expect(onRatioChange).not.toHaveBeenCalled()
    })

    it('cancels in-flight drag without committing when a child is swapped', () => {
      const [layout, setLayout] = createSignal<LayoutNodeLocal>({
        type: 'split',
        id: 'sp',
        direction: 'vertical',
        ratios: [0.5, 0.5],
        children: [
          { type: 'leaf', id: 'a' },
          { type: 'leaf', id: 'b' },
        ],
      })
      const onRatioChange = vi.fn()
      const { container } = render(() => (
        <TilingLayout root={layout()} renderTile={renderTile} onRatioChange={onRatioChange} />
      ))
      const split = container.querySelector('[data-testid="tile-split"]') as HTMLElement
      stubBoundingRect(split, 1000, 600)

      const handle = container.querySelector('[data-testid="tile-resize-handle"]') as HTMLElement
      dispatchPointerDown(handle, { x: 0 })
      dispatchPointerMove({ x: 100 })

      // Swap a child id (not the ratios) — the structural-cancel effect
      // tracks `children` so it still fires.
      setLayout({
        type: 'split',
        id: 'sp',
        direction: 'vertical',
        ratios: [0.5, 0.5],
        children: [
          { type: 'leaf', id: 'a' },
          { type: 'leaf', id: 'c' },
        ],
      })

      dispatchPointerUp({ x: 100 })
      expect(onRatioChange).not.toHaveBeenCalled()
    })
  })

  describe('grid renderer', () => {
    function makeGrid(): LayoutNodeLocal {
      return {
        type: 'grid',
        id: 'g',
        rows: 2,
        cols: 2,
        rowRatios: [0.5, 0.5],
        colRatios: [0.5, 0.5],
        cells: [
          { type: 'leaf', id: 'c00' },
          { type: 'leaf', id: 'c01' },
          { type: 'leaf', id: 'c10' },
          { type: 'leaf', id: 'c11' },
        ],
      }
    }

    it('emits live-preview col ratios via inline grid-template-columns and commits once on pointerup', () => {
      const onGridRatiosChange = vi.fn()
      const { container } = render(() => (
        <TilingLayout root={makeGrid()} renderTile={renderTile} onGridRatiosChange={onGridRatiosChange} />
      ))
      const grid = container.querySelector('[data-testid="tile-grid"]') as HTMLElement
      stubBoundingRect(grid, 1000, 600)

      const colHandle = container.querySelector('[data-testid="grid-resize-handle"][data-axis="col"]') as HTMLElement
      expect(colHandle).not.toBeNull()
      dispatchPointerDown(colHandle, { x: 500 })
      dispatchPointerMove({ x: 700 })
      expect(grid.style.getPropertyValue('grid-template-columns')).toMatch(/0\.7.*?fr.*?0\.3.*?fr/)
      expect(onGridRatiosChange).not.toHaveBeenCalled()

      dispatchPointerUp({ x: 700 })
      expect(onGridRatiosChange).toHaveBeenCalledTimes(1)
      expect(onGridRatiosChange.mock.calls[0]?.[0]).toBe('g')
      expect(onGridRatiosChange.mock.calls[0]?.[1]).toBe('col')
      const finalRatios = onGridRatiosChange.mock.calls[0]?.[2] as number[]
      expect(finalRatios[0]).toBeCloseTo(0.7, 9)
      expect(finalRatios[1]).toBeCloseTo(0.3, 9)
    })

    it('exposes role="separator" with the correct aria-orientation per axis', () => {
      const { container } = render(() => (
        <TilingLayout root={makeGrid()} renderTile={renderTile} />
      ))
      const colHandle = container.querySelector('[data-testid="grid-resize-handle"][data-axis="col"]') as HTMLElement
      const rowHandle = container.querySelector('[data-testid="grid-resize-handle"][data-axis="row"]') as HTMLElement
      expect(colHandle.getAttribute('role')).toBe('separator')
      expect(colHandle.getAttribute('aria-orientation')).toBe('vertical')
      expect(rowHandle.getAttribute('role')).toBe('separator')
      expect(rowHandle.getAttribute('aria-orientation')).toBe('horizontal')
    })
  })

  it('preserves tile DOM identity for unchanged children when split structure changes', () => {
    // Regression guard for the splitKey/keyed-remount removal: when a split
    // gains a new child, the existing children must NOT remount. This is
    // what makes the screenApplied set in TerminalView the right contract
    // — terminals that didn't actually move should keep their state.
    const leafA: LayoutNodeLocal = { type: 'leaf', id: 'a' }
    const leafB: LayoutNodeLocal = { type: 'leaf', id: 'b' }
    const leafC: LayoutNodeLocal = { type: 'leaf', id: 'c' }
    const [layout, setLayout] = createSignal<LayoutNodeLocal>({
      type: 'split',
      id: 'sp',
      direction: 'vertical',
      ratios: [0.5, 0.5],
      children: [leafA, leafB],
    })
    const { container } = render(() => (
      <TilingLayout root={layout()} renderTile={renderTile} />
    ))
    const tileABefore = container.querySelector('[data-testid="tile-a"]')
    const tileBBefore = container.querySelector('[data-testid="tile-b"]')
    expect(tileABefore).not.toBeNull()
    expect(tileBBefore).not.toBeNull()

    // Add a third child while preserving the existing leaf object
    // identities. <For> keyed by child identity must not remount the
    // existing tiles.
    setLayout({
      type: 'split',
      id: 'sp',
      direction: 'vertical',
      ratios: [0.4, 0.4, 0.2],
      children: [leafA, leafB, leafC],
    })

    const tileAAfter = container.querySelector('[data-testid="tile-a"]')
    const tileBAfter = container.querySelector('[data-testid="tile-b"]')
    const tileCAfter = container.querySelector('[data-testid="tile-c"]')
    expect(tileAAfter).toBe(tileABefore)
    expect(tileBAfter).toBe(tileBBefore)
    expect(tileCAfter).not.toBeNull()
  })

  it('cleans up window listeners and data-dragging on unmount mid-drag', () => {
    const root: LayoutNodeLocal = {
      type: 'split',
      id: 'sp',
      direction: 'vertical',
      ratios: [0.5, 0.5],
      children: [
        { type: 'leaf', id: 'a' },
        { type: 'leaf', id: 'b' },
      ],
    }
    const onRatioChange = vi.fn()
    const { container, unmount } = render(() => (
      <TilingLayout root={root} renderTile={renderTile} onRatioChange={onRatioChange} />
    ))
    const split = container.querySelector('[data-testid="tile-split"]') as HTMLElement
    stubBoundingRect(split, 1000, 600)
    const handle = container.querySelector('[data-testid="tile-resize-handle"]') as HTMLElement
    dispatchPointerDown(handle, { x: 0 })
    dispatchPointerMove({ x: 100 })
    expect(handle.dataset.dragging).toBe('')

    unmount()

    // onCleanup ran the helper teardown; data-dragging must be cleared on
    // the (now-detached) handle.
    expect(handle.dataset.dragging).toBeUndefined()
    // Subsequent events must not throw and must not commit.
    expect(() => dispatchPointerMove({ x: 200 })).not.toThrow()
    expect(() => dispatchPointerUp({ x: 200 })).not.toThrow()
    expect(onRatioChange).not.toHaveBeenCalled()
  })
})
