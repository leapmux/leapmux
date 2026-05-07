import type { PairDragOptions } from '~/components/shell/useTileDragResize'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { startPairRebalanceDrag } from '~/components/shell/useTileDragResize'
import { MIN_SPLIT_RATIO } from '~/stores/layout.store'
import {
  dispatchPointerCancel,
  dispatchPointerMove,
  dispatchPointerUp,
  installPointerEventShim,
  pointerdownEvent,
  stubBoundingRect,
} from '../helpers/pointer'

beforeAll(installPointerEventShim)

interface Scaffold {
  container: HTMLDivElement
  handle: HTMLDivElement
}

function makeScaffold({ width = 1000, height = 600 } = {}): Scaffold {
  const container = document.createElement('div')
  const handle = document.createElement('div')
  document.body.appendChild(container)
  container.appendChild(handle)
  stubBoundingRect(container, width, height)
  // The helper reads `startEvent.currentTarget` for `data-dragging`
  // bookkeeping; emulate the synthetic pointerdown landing on `handle`.
  Object.defineProperty(handle, 'isConnected', { value: true })
  return { container, handle }
}

function start(
  scaffold: Scaffold,
  overrides: Partial<PairDragOptions> & { startRatios: readonly number[] },
): { commit: ReturnType<typeof vi.fn>, setDragRatios: ReturnType<typeof vi.fn>, onDone: ReturnType<typeof vi.fn>, teardown: (() => void) | null } {
  const commit = vi.fn()
  const setDragRatios = vi.fn()
  const onDone = vi.fn()
  const startEvent = pointerdownEvent()
  // dispatchEvent(handle, ...) would require dispatching, but we just need
  // currentTarget set; defineProperty does that without firing listeners.
  Object.defineProperty(startEvent, 'currentTarget', { value: scaffold.handle })
  const teardown = startPairRebalanceDrag({
    axis: 'col',
    index: 0,
    startEvent,
    containerRef: scaffold.container,
    setDragRatios,
    commit,
    onDone,
    ...overrides,
  })
  return { commit, setDragRatios, onDone, teardown }
}

describe('startPairRebalanceDrag', () => {
  let scaffold: Scaffold

  beforeEach(() => {
    scaffold = makeScaffold()
  })

  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('rebalances the pair on pointermove and commits once on pointerup', () => {
    const { commit, setDragRatios, teardown } = start(scaffold, {
      startRatios: [0.5, 0.5],
    })
    expect(teardown).not.toBeNull()
    // Drag 200px right on a 1000px container → +0.2 to ratios[0].
    dispatchPointerMove({ x: 200 })
    expect(setDragRatios).toHaveBeenCalledTimes(1)
    expect(setDragRatios.mock.calls[0]?.[0]).toEqual([0.7, 0.30000000000000004])
    expect(commit).not.toHaveBeenCalled()
    dispatchPointerUp({ x: 200 })
    expect(commit).toHaveBeenCalledTimes(1)
    expect(commit.mock.calls[0]?.[0]).toEqual([0.7, 0.30000000000000004])
    // Final setDragRatios(null) clears the live preview.
    expect(setDragRatios).toHaveBeenLastCalledWith(null)
  })

  it('does not commit on pointerdown→pointerup with no movement', () => {
    const { commit, setDragRatios } = start(scaffold, {
      startRatios: [0.5, 0.5],
    })
    dispatchPointerUp()
    expect(commit).not.toHaveBeenCalled()
    // setDragRatios should still receive the null clear.
    expect(setDragRatios).toHaveBeenCalledWith(null)
  })

  it('clamps the smaller side at MIN_SPLIT_RATIO and preserves the pair sum', () => {
    const { commit, setDragRatios } = start(scaffold, {
      startRatios: [0.5, 0.5],
    })
    // Drag far past the floor.
    dispatchPointerMove({ x: 9999 })
    const previewed = setDragRatios.mock.calls[0]?.[0] as number[]
    expect(previewed[1]).toBeCloseTo(MIN_SPLIT_RATIO, 12)
    expect(previewed[0] + previewed[1]).toBeCloseTo(1, 9)
    dispatchPointerUp({ x: 9999 })
    expect(commit).toHaveBeenCalledTimes(1)
    const committed = commit.mock.calls[0]?.[0] as number[]
    expect(committed[1]).toBeCloseTo(MIN_SPLIT_RATIO, 12)
    expect(committed[0] + committed[1]).toBeCloseTo(1, 9)
  })

  it('returns null when the pair sum is below 2× MIN_SPLIT_RATIO', () => {
    // Pair sum 0.04 + 0.05 = 0.09 < 0.10
    const { teardown, commit } = start(scaffold, {
      startRatios: [0.04, 0.05, 0.91],
    })
    expect(teardown).toBeNull()
    // Verify no listeners attached: a subsequent move/up should be inert.
    dispatchPointerMove({ x: 100 })
    dispatchPointerUp({ x: 100 })
    expect(commit).not.toHaveBeenCalled()
  })

  it('only mutates the (i, i+1) pair for N>2 ratios', () => {
    const { commit } = start(scaffold, {
      index: 1,
      startRatios: [0.3, 0.3, 0.4],
    })
    dispatchPointerMove({ x: 100 })
    dispatchPointerUp({ x: 100 })
    expect(commit).toHaveBeenCalledTimes(1)
    const [c] = commit.mock.calls[0] as [number[]]
    // ratios[0] is left untouched.
    expect(c[0]).toBe(0.3)
    // (1, 2) rebalanced; sum preserved.
    expect(c[1] + c[2]).toBeCloseTo(0.7, 9)
    expect(c[1]).toBeCloseTo(0.4, 9)
    expect(c[2]).toBeCloseTo(0.3, 9)
  })

  it('commits on pointercancel after a movement', () => {
    const { commit } = start(scaffold, {
      startRatios: [0.5, 0.5],
    })
    dispatchPointerMove({ x: 100 })
    dispatchPointerCancel()
    expect(commit).toHaveBeenCalledTimes(1)
  })

  it('teardown aborts without committing and is idempotent', () => {
    const { commit, setDragRatios, teardown, onDone } = start(scaffold, {
      startRatios: [0.5, 0.5],
    })
    dispatchPointerMove({ x: 100 })
    expect(setDragRatios).toHaveBeenCalledTimes(1)
    teardown!()
    expect(commit).not.toHaveBeenCalled()
    expect(onDone).toHaveBeenCalledTimes(1)
    // Subsequent pointermove/up are inert.
    dispatchPointerMove({ x: 200 })
    dispatchPointerUp({ x: 200 })
    expect(setDragRatios).toHaveBeenLastCalledWith(null)
    expect(setDragRatios.mock.calls.filter(c => Array.isArray(c[0])).length).toBe(1)
    // Teardown is idempotent.
    teardown!()
    expect(onDone).toHaveBeenCalledTimes(1)
  })

  it('returns null when the container has zero width', () => {
    stubBoundingRect(scaffold.container, 0, 600)
    const { teardown } = start(scaffold, { startRatios: [0.5, 0.5] })
    expect(teardown).toBeNull()
  })

  it('manages the data-dragging attribute through the lifecycle', () => {
    const { teardown } = start(scaffold, {
      startRatios: [0.5, 0.5],
    })
    expect(scaffold.handle.dataset.dragging).toBe('')
    dispatchPointerMove({ x: 50 })
    expect(scaffold.handle.dataset.dragging).toBe('')
    dispatchPointerUp({ x: 50 })
    expect(scaffold.handle.dataset.dragging).toBeUndefined()
    // Restart and verify external teardown also clears the attribute.
    const second = start(scaffold, { startRatios: [0.5, 0.5] })
    expect(scaffold.handle.dataset.dragging).toBe('')
    second.teardown!()
    expect(scaffold.handle.dataset.dragging).toBeUndefined()
    // pointercancel clears the attribute too.
    start(scaffold, { startRatios: [0.5, 0.5] })
    expect(scaffold.handle.dataset.dragging).toBe('')
    dispatchPointerCancel()
    expect(scaffold.handle.dataset.dragging).toBeUndefined()
    expect(teardown).not.toBeNull()
  })

  it('ignores pointermoves from a different pointerId', () => {
    const { setDragRatios, commit } = start(scaffold, {
      startRatios: [0.5, 0.5],
    })
    // A second touch with pointerId=2 should not affect the drag.
    dispatchPointerMove({ x: 200, pointerId: 2 })
    expect(setDragRatios).not.toHaveBeenCalled()
    // The original pointerId still drives the drag.
    dispatchPointerMove({ x: 100, pointerId: 1 })
    expect(setDragRatios).toHaveBeenCalledTimes(1)
    // pointerup from pointerId=2 is also ignored.
    dispatchPointerUp({ x: 200, pointerId: 2 })
    expect(commit).not.toHaveBeenCalled()
    dispatchPointerUp({ x: 100, pointerId: 1 })
    expect(commit).toHaveBeenCalledTimes(1)
  })

  it('drags on the row axis using clientY', () => {
    const { commit } = start(scaffold, {
      axis: 'row',
      startRatios: [0.5, 0.5],
    })
    // 600px tall container; +120px Y → +0.2 ratio.
    dispatchPointerMove({ y: 120 })
    dispatchPointerUp({ y: 120 })
    const [committed] = commit.mock.calls[0] as [number[]]
    expect(committed[0]).toBeCloseTo(0.7, 9)
    expect(committed[1]).toBeCloseTo(0.3, 9)
  })
})
