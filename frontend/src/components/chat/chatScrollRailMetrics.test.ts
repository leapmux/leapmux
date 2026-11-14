import type { Accessor } from 'solid-js'
import type { RailMetricsInputs } from './chatScrollRailMetrics'
import { createEffect, createSignal, on } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { inRoot, settle } from '~/test-support/async'
import { createRailMetrics } from './chatScrollRailMetrics'
import { installScrollTestEnv, makeFakeScrollDiv } from './useChatScroll.testkit'

installScrollTestEnv()

/** The virtualizer state that the metrics read. */
interface GeometryFixture {
  /** The virtualizer's total content height (px). */
  totalHeight: Accessor<number>
  geometryVersion?: Accessor<number>
  layoutShift?: Accessor<number>
}

/** The scroll container and the virtualizer state that one test mounts the metrics over. */
interface MetricsFixture extends GeometryFixture {
  scrollHeight: number
  clientHeight: number
  scrollTop?: number
}

/**
 * The geometry input of createRailMetrics. Its `contentTopOf` follows from the layout shift
 * (`content = scroll - layoutShift`), as the virtualizer's does, so no test can hand the metrics a
 * conversion that disagrees with the shift it reports.
 */
function railGeometry(o: GeometryFixture): ReturnType<RailMetricsInputs['geometry']> {
  const layoutShift = o.layoutShift ?? (() => 0)
  return {
    totalHeight: o.totalHeight,
    geometryVersion: o.geometryVersion ?? (() => 0),
    contentTopOf: (scrollTop: number) => scrollTop - layoutShift(),
    layoutShift,
  }
}

/** Mount createRailMetrics over a fake scroll container (see railGeometry). */
function mountMetrics(o: MetricsFixture) {
  const div = makeFakeScrollDiv()
  div.setScrollHeight(o.scrollHeight)
  div.setClientHeight(o.clientHeight)
  div.setScrollTop(o.scrollTop ?? 0)
  const geometry = railGeometry(o)
  const metrics = createRailMetrics({
    scrollEl: () => div.el,
    geometry: () => geometry,
  })
  return { div, metrics }
}

describe('createRailMetrics', () => {
  it('samples the scroll container, and re-samples on a scroll event (after the coalescing frame)', () =>
    inRoot(async () => {
      const { div, metrics } = mountMetrics({
        scrollHeight: 1000,
        clientHeight: 400,
        totalHeight: () => 1000,
      })
      await settle()
      expect(metrics()).toEqual({ contentTop: 0, dist: 600, clientHeight: 400 }) // 1000 - 0 - 400

      div.setScrollTop(250)
      div.el.dispatchEvent(new Event('scroll'))
      await settle() // the scroll handler coalesces through a rAF (a microtask in the test env)
      expect(metrics()).toEqual({ contentTop: 250, dist: 350, clientHeight: 400 })
    }))

  it('re-samples on a geometry commit that moved the offset map without a scroll event', () =>
    inRoot(async () => {
      const [geo, setGeo] = createSignal(0)
      const { div, metrics } = mountMetrics({
        scrollHeight: 1000,
        clientHeight: 400,
        totalHeight: () => 1000,
        geometryVersion: geo,
      })
      await settle()

      // A row-height commit moves the content without dispatching a scroll event.
      div.setScrollTop(120)
      await settle()
      expect(metrics().contentTop).toBe(0) // still stale: no scroll event, no geometry commit yet
      setGeo(1) // the virtualizer bumps geometryVersion on the commit
      await settle()
      expect(metrics().contentTop).toBe(120)
    }))

  it('re-samples when the total height changes without a scroll event', () =>
    inRoot(async () => {
      const [total, setTotal] = createSignal(1000)
      const { div, metrics } = mountMetrics({ scrollHeight: 1000, clientHeight: 400, totalHeight: total })
      await settle()

      // The content grows and the view moves, and no scroll event fires.
      div.setScrollHeight(1200)
      div.setScrollTop(300)
      await settle()
      expect(metrics().contentTop).toBe(0) // still stale: no scroll event, no new total yet
      setTotal(1200)
      await settle()
      expect(metrics()).toEqual({ contentTop: 300, dist: 500, clientHeight: 400 }) // 1200 - 300 - 400
    }))

  it('defers a geometry-commit re-sample to a coalescing rAF (not a synchronous per-commit layout read)', () =>
    inRoot(async () => {
      const [geo, setGeo] = createSignal(0)
      const { div, metrics } = mountMetrics({
        scrollHeight: 1000,
        clientHeight: 400,
        totalHeight: () => 1000,
        geometryVersion: geo,
      })
      await settle()

      const rafSpy = vi.spyOn(globalThis, 'requestAnimationFrame')
      // A burst of commits before the frame flushes: the re-sample is DEFERRED (the position
      // stays stale synchronously, not read per commit) and the burst COALESCES to one frame.
      div.setScrollTop(200)
      setGeo(1)
      setGeo(2)
      expect(metrics().contentTop).toBe(0) // deferred: no synchronous layout read on the commit
      await settle()
      expect(rafSpy).toHaveBeenCalledTimes(1) // one frame for the whole burst
      expect(metrics().contentTop).toBe(200) // the single frame sampled the final position
      rafSpy.mockRestore()
    }))

  it('samples at once where requestAnimationFrame is absent', () =>
    inRoot(async () => {
      const [geo, setGeo] = createSignal(0)
      const { div, metrics } = mountMetrics({
        scrollHeight: 1000,
        clientHeight: 400,
        totalHeight: () => 1000,
        geometryVersion: geo,
      })
      await settle()

      vi.stubGlobal('requestAnimationFrame', undefined)
      vi.stubGlobal('cancelAnimationFrame', undefined)
      try {
        div.setScrollTop(200)
        setGeo(1)
        expect(metrics().contentTop).toBe(200)

        div.setScrollTop(250)
        div.el.dispatchEvent(new Event('scroll'))
        expect(metrics().contentTop).toBe(250)
      }
      finally {
        vi.unstubAllGlobals()
      }
    }))

  it('stays at the default and does not crash when the scroll element is absent (pre-mount)', () =>
    inRoot(async () => {
      // props.scrollEl is undefined until the chat container mounts; sampling must no-op.
      const metrics = createRailMetrics({
        scrollEl: () => undefined,
        geometry: () => ({
          totalHeight: () => 1000,
          geometryVersion: () => 0,
          contentTopOf: scrollTop => scrollTop,
          layoutShift: () => 0,
        }),
      })
      await settle()
      expect(metrics()).toEqual({ contentTop: 0, dist: 0, clientHeight: 0 })
    }))

  it('does not notify subscribers on an identical re-sample (value equals)', () =>
    inRoot(async () => {
      const [geo, setGeo] = createSignal(0)
      const { metrics } = mountMetrics({
        scrollHeight: 1000,
        clientHeight: 400,
        scrollTop: 80,
        totalHeight: () => 1000,
        geometryVersion: geo,
      })
      let notifications = 0
      createEffect(on(metrics, () => {
        notifications += 1
      }))
      await settle()
      const baseline = notifications

      // A geometry commit that re-samples the SAME scroll position must not invalidate the
      // thumb memos downstream -- the value equals keeps the old reference.
      setGeo(1)
      await settle()
      expect(notifications).toBe(baseline)
    }))

  it('converts the sampled position to content coordinates', () =>
    inRoot(async () => {
      const { metrics } = mountMetrics({
        scrollHeight: 2000,
        clientHeight: 400,
        scrollTop: 500,
        totalHeight: () => 2300,
        layoutShift: () => -300,
      })
      await settle()
      // The distance from the bottom stays a scroll-coordinate distance.
      expect(metrics()).toEqual({ contentTop: 800, dist: 1100, clientHeight: 400 })
    }))

  it('floors a position above the first row at 0', () =>
    inRoot(async () => {
      const { metrics } = mountMetrics({
        scrollHeight: 2000,
        clientHeight: 400,
        scrollTop: 100,
        totalHeight: () => 1800,
        layoutShift: () => 200,
      })
      await settle()
      expect(metrics().contentTop).toBe(0)
    }))

  it('samples again when the shift changes without a scroll', () =>
    inRoot(async () => {
      const [shift, setShift] = createSignal(0)
      const { metrics } = mountMetrics({
        scrollHeight: 2000,
        clientHeight: 400,
        scrollTop: 500,
        totalHeight: () => 2000,
        layoutShift: shift,
      })
      await settle()
      expect(metrics().contentTop).toBe(500)

      // A row above measured: the layer moved by -300 and no scroll event fired.
      setShift(-300)
      await settle()
      expect(metrics().contentTop).toBe(800)
    }))

  it('clamps a rubber-band overscroll into the scroll range before it converts the position', () =>
    inRoot(async () => {
      // Content 0 sits at scroll coordinate -300, so a position above the scroll range would
      // convert to a content coordinate that the floor at 0 does not catch.
      const { div, metrics } = mountMetrics({
        scrollHeight: 1000,
        clientHeight: 400,
        totalHeight: () => 1300,
        layoutShift: () => -300,
      })
      // WebKit reports the overscroll of an elastic scroll as a scrollTop outside [0, 600].
      const overscroll = (scrollTop: number) => {
        Object.defineProperty(div.el, 'scrollTop', {
          configurable: true,
          get: () => scrollTop,
        })
        div.el.dispatchEvent(new Event('scroll'))
      }

      overscroll(-40)
      await settle()
      expect(metrics().contentTop).toBe(300) // scroll 0, not -40

      overscroll(700)
      await settle()
      expect(metrics().contentTop).toBe(900) // scroll 600, the end of the range, not 700
    }))

  it('samples with the conversion of a geometry that replaces the previous one', () =>
    inRoot(async () => {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(2000)
      div.setClientHeight(400)
      div.setScrollTop(500)
      const [geometry, setGeometry] = createSignal(railGeometry({
        totalHeight: () => 2000,
      }))
      const metrics = createRailMetrics({
        scrollEl: () => div.el,
        geometry,
      })
      await settle()
      expect(metrics().contentTop).toBe(500)

      // A geometry with a shift of -300 replaces the first one, and no scroll event fires.
      setGeometry(railGeometry({
        totalHeight: () => 2300,
        layoutShift: () => -300,
      }))
      await settle()
      expect(metrics().contentTop).toBe(800)
    }))
})
