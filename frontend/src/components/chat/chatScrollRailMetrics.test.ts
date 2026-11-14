import { createEffect, createRoot, createSignal, on } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { createRailMetrics } from './chatScrollRailMetrics'
import { installScrollTestEnv, makeFakeScrollDiv } from './useChatScroll.testkit'

installScrollTestEnv()

/** Flush pending microtasks + Solid effects (setTimeout runs after the effect scheduler + microtask rAF). */
const tick = () => new Promise<void>(resolve => setTimeout(resolve, 0))

function inRoot(body: (dispose: () => void) => Promise<void>): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    createRoot(async (dispose) => {
      try {
        await body(dispose)
        dispose()
        resolve()
      }
      catch (e) {
        dispose()
        reject(e instanceof Error
          ? e
          : new Error(String(e)))
      }
    })
  })
}

describe('createRailMetrics', () => {
  it('samples the scroll container, and re-samples on a scroll event (after the coalescing frame)', () =>
    inRoot(async () => {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(1000)
      div.setClientHeight(400)
      div.setScrollTop(0)
      const [scrollEl] = createSignal<HTMLDivElement | undefined>(div.el)
      const metrics = createRailMetrics({
        scrollEl,
        totalHeight: () => 1000,
        geometryVersion: () => 0,
        contentTopOf: scrollTop => scrollTop,
        layoutShift: () => 0,
      })
      await tick()
      expect(metrics()).toEqual({ scrollTop: 0, dist: 600, clientHeight: 400 }) // 1000 - 0 - 400

      div.setScrollTop(250)
      div.el.dispatchEvent(new Event('scroll'))
      await tick() // the scroll handler coalesces through a rAF (a microtask in the test env)
      expect(metrics()).toEqual({ scrollTop: 250, dist: 350, clientHeight: 400 })
    }))

  it('re-samples on a geometry commit that moved the offset map without a scroll event', () =>
    inRoot(async () => {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(1000)
      div.setClientHeight(400)
      const [scrollEl] = createSignal<HTMLDivElement | undefined>(div.el)
      const [geo, setGeo] = createSignal(0)
      const metrics = createRailMetrics({
        scrollEl,
        totalHeight: () => 1000,
        geometryVersion: geo,
        contentTopOf: scrollTop => scrollTop,
        layoutShift: () => 0,
      })
      await tick()

      // A prepend/measurement shifts the view without dispatching a scroll event.
      div.setScrollTop(120)
      await tick()
      expect(metrics().scrollTop).toBe(0) // still stale: no scroll event, no geometry commit yet
      setGeo(1) // the virtualizer bumps geometryVersion on the commit
      await tick()
      expect(metrics().scrollTop).toBe(120)
    }))

  it('defers a geometry-commit re-sample to a coalescing rAF (not a synchronous per-commit layout read)', () =>
    inRoot(async () => {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(1000)
      div.setClientHeight(400)
      const [scrollEl] = createSignal<HTMLDivElement | undefined>(div.el)
      const [geo, setGeo] = createSignal(0)
      const metrics = createRailMetrics({
        scrollEl,
        totalHeight: () => 1000,
        geometryVersion: geo,
        contentTopOf: scrollTop => scrollTop,
        layoutShift: () => 0,
      })
      await tick()

      const rafSpy = vi.spyOn(globalThis, 'requestAnimationFrame')
      // A burst of commits before the frame flushes: the re-sample is DEFERRED (scrollTop
      // stays stale synchronously, not read per commit) and the burst COALESCES to one frame.
      div.setScrollTop(200)
      setGeo(1)
      setGeo(2)
      expect(metrics().scrollTop).toBe(0) // deferred: no synchronous layout read on the commit
      await tick()
      expect(rafSpy).toHaveBeenCalledTimes(1) // one frame for the whole burst
      expect(metrics().scrollTop).toBe(200) // the single frame sampled the final position
      rafSpy.mockRestore()
    }))

  it('stays at the default and does not crash when the scroll element is absent (pre-mount)', () =>
    inRoot(async () => {
      // props.scrollEl is undefined until the chat container mounts; sampling must no-op.
      const metrics = createRailMetrics({
        scrollEl: () => undefined,
        totalHeight: () => 1000,
        geometryVersion: () => 0,
        contentTopOf: scrollTop => scrollTop,
        layoutShift: () => 0,
      })
      await tick()
      expect(metrics()).toEqual({ scrollTop: 0, dist: 0, clientHeight: 0 })
    }))

  it('does not notify subscribers on an identical re-sample (value equals)', () =>
    inRoot(async () => {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(1000)
      div.setClientHeight(400)
      div.setScrollTop(80)
      const [scrollEl] = createSignal<HTMLDivElement | undefined>(div.el)
      const [geo, setGeo] = createSignal(0)
      const metrics = createRailMetrics({
        scrollEl,
        totalHeight: () => 1000,
        geometryVersion: geo,
        contentTopOf: scrollTop => scrollTop,
        layoutShift: () => 0,
      })
      let notifications = 0
      createEffect(on(metrics, () => {
        notifications += 1
      }))
      await tick()
      const baseline = notifications

      // A geometry commit that re-samples the SAME scroll position must not invalidate the
      // thumb memos downstream -- the value equals keeps the old reference.
      setGeo(1)
      await tick()
      expect(notifications).toBe(baseline)
    }))

  it('converts the sampled position to content coordinates', () =>
    inRoot(async () => {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(2000)
      div.setClientHeight(400)
      div.setScrollTop(500)
      const [scrollEl] = createSignal<HTMLDivElement | undefined>(div.el)
      const metrics = createRailMetrics({
        scrollEl,
        totalHeight: () => 2300,
        geometryVersion: () => 0,
        contentTopOf: scrollTop => scrollTop + 300,
        layoutShift: () => -300,
      })
      await tick()
      // The distance from the bottom stays a scroll-coordinate distance.
      expect(metrics()).toEqual({ scrollTop: 800, dist: 1100, clientHeight: 400 })
    }))

  it('floors a position above the first row at 0', () =>
    inRoot(async () => {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(2000)
      div.setClientHeight(400)
      div.setScrollTop(100)
      const [scrollEl] = createSignal<HTMLDivElement | undefined>(div.el)
      const metrics = createRailMetrics({
        scrollEl,
        totalHeight: () => 1800,
        geometryVersion: () => 0,
        contentTopOf: scrollTop => scrollTop - 200,
        layoutShift: () => 200,
      })
      await tick()
      expect(metrics().scrollTop).toBe(0)
    }))

  it('samples again when the shift changes without a scroll', () =>
    inRoot(async () => {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(2000)
      div.setClientHeight(400)
      div.setScrollTop(500)
      const [scrollEl] = createSignal<HTMLDivElement | undefined>(div.el)
      const [shift, setShift] = createSignal(0)
      const metrics = createRailMetrics({
        scrollEl,
        totalHeight: () => 2000,
        geometryVersion: () => 0,
        contentTopOf: scrollTop => scrollTop - shift(),
        layoutShift: shift,
      })
      await tick()
      expect(metrics().scrollTop).toBe(500)

      // A row above measured: the layer moved by -300 and no scroll event fired.
      setShift(-300)
      await tick()
      expect(metrics().scrollTop).toBe(800)
    }))
})
