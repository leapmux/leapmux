import { render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useCollapsedOverflow } from './collapsedOverflow'

/** A ResizeObserver stand-in that counts the observers that the hook builds and ends. */
function countResizeObservers(): { built: () => number, disconnected: () => number } {
  let built = 0
  let disconnected = 0
  vi.stubGlobal('ResizeObserver', class {
    constructor() {
      built++
    }

    observe() {}
    unobserve() {}
    disconnect() {
      disconnected++
    }
  })
  return {
    built: () => built,
    disconnected: () => disconnected,
  }
}

/** Mount one box whose clip, collapsed state and watched input the test drives. */
function mountBox(opts: { collapsed?: boolean, clips?: boolean } = {}) {
  const [collapsed, setCollapsed] = createSignal(opts.collapsed ?? true)
  const [clips, setClips] = createSignal(opts.clips ?? false)
  const [watched, setWatched] = createSignal(0)
  const reports: boolean[] = []
  let measures = 0
  function Box() {
    const overflow = useCollapsedOverflow({
      collapsed,
      clips: () => {
        measures++
        return clips()
      },
      watch: (_element, measure) => {
        watched()
        measure()
      },
      onOverflowChange: next => reports.push(next),
    })
    return <div ref={overflow.ref} />
  }
  const view = render(() => <Box />)
  return {
    unmount: view.unmount,
    reports,
    setCollapsed,
    setClips,
    setWatched,
    measures: () => measures,
  }
}

describe('useCollapsedOverflow', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('reports a collapsed box that clips, and only a change after that', () => {
    const box = mountBox({ clips: true })
    expect(box.reports).toEqual([true])
    box.setWatched(1)
    expect(box.reports).toEqual([true])
    box.setClips(false)
    box.setWatched(2)
    expect(box.reports).toEqual([true, false])
  })

  // An expanded box clips nothing. Reporting false then would take the control away from
  // the row that the reader just expanded.
  it('keeps the last collapsed answer while the box is expanded', () => {
    const box = mountBox({ clips: true })
    box.setCollapsed(false)
    box.setClips(false)
    expect(box.reports).toEqual([true])
    box.setCollapsed(true)
    expect(box.reports).toEqual([true, false])
  })

  // The expand state of a row outlives a remount, so a box can mount expanded with no
  // collapsed answer yet.
  it('reports a clip for a box that mounts expanded, until it collapses and measures', () => {
    const box = mountBox({ collapsed: false })
    expect(box.reports).toEqual([true])
    box.setCollapsed(true)
    expect(box.reports).toEqual([true, false])
  })

  // The caller's watch runs in a scope of its own: what it tracks measures the box again,
  // and keeps the size observer.
  it('measures again for a change that the watch tracks, and keeps the size observer', () => {
    const observers = countResizeObservers()
    const box = mountBox()
    const before = box.measures()
    expect(observers.built()).toBe(1)
    box.setWatched(1)
    expect(box.measures()).toBeGreaterThan(before)
    expect(observers.built()).toBe(1)
  })

  // An expanded box clips nothing, so nothing measures it: the size observer ends, and a
  // change that the watch tracked measures nothing.
  it('stops measuring while the box is expanded', () => {
    const observers = countResizeObservers()
    const box = mountBox()
    expect(observers.disconnected()).toBe(0)
    box.setCollapsed(false)
    expect(observers.disconnected()).toBe(1)
    const measures = box.measures()
    box.setWatched(1)
    expect(box.measures()).toBe(measures)
  })

  it('ends the size observer when the box unmounts', () => {
    const observers = countResizeObservers()
    const box = mountBox()
    box.unmount()
    expect(observers.disconnected()).toBe(1)
  })
})
