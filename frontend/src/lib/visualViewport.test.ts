import { describe, expect, it, onTestFinished, vi } from 'vitest'
import { createManualFrames } from '~/test-support/manualFrames'
import { createMockVisualViewport } from '~/test-support/visualViewportStub'
import {
  readViewportRect,
  registerVisualViewportInsetUpdater,
  scheduleVisualViewportUpdate,
  subscribeVisualViewport,
} from './visualViewport'

describe('readViewportRect', () => {
  it('returns zero edges when window is undefined', () => {
    vi.stubGlobal('window', undefined)
    onTestFinished(() => {
      vi.unstubAllGlobals()
    })
    expect(readViewportRect()).toEqual({ top: 0, left: 0, bottom: 0, right: 0 })
  })

  it('falls back atomically to window inner dimensions when visualViewport is zero-sized', () => {
    vi.stubGlobal('innerWidth', 800)
    vi.stubGlobal('innerHeight', 500)
    vi.stubGlobal('visualViewport', {
      width: 0,
      height: 0,
      offsetTop: 452,
      offsetLeft: 80,
    })
    onTestFinished(() => {
      vi.unstubAllGlobals()
    })
    expect(readViewportRect()).toEqual({
      top: 0,
      left: 0,
      bottom: 500,
      right: 800,
    })
  })

  it('honors negative --vv-shift over visualViewport.offsetTop', () => {
    vi.stubGlobal('innerWidth', 390)
    vi.stubGlobal('innerHeight', 800)
    vi.stubGlobal('visualViewport', {
      width: 390,
      height: 800,
      offsetTop: 120,
      offsetLeft: 0,
    })
    document.documentElement.style.setProperty('--vv-shift', '-120px')
    onTestFinished(() => {
      vi.unstubAllGlobals()
      document.documentElement.style.removeProperty('--vv-shift')
    })
    expect(readViewportRect()).toEqual({
      top: -120,
      left: 0,
      bottom: 680,
      right: 390,
    })
  })
})

describe('visualViewport subscription hub', () => {
  it('runs the phase-1 inset updater before phase-2 subscribers in one animation frame', () => {
    const vv = createMockVisualViewport(800, 0)
    const frames = createManualFrames()
    vi.stubGlobal('visualViewport', vv)
    vi.stubGlobal('requestAnimationFrame', frames.requestFrame)
    vi.stubGlobal('cancelAnimationFrame', frames.cancelFrame)
    onTestFinished(() => {
      vi.unstubAllGlobals()
    })

    const order: string[] = []
    const unregUpdater = registerVisualViewportInsetUpdater(() => {
      order.push('phase1-inset')
      return true
    })
    const unsubOverlay = subscribeVisualViewport((change) => {
      order.push(`phase2-overlay:${change.resized}`)
    })
    onTestFinished(() => {
      unsubOverlay()
      unregUpdater()
    })

    vv.dispatch('resize')
    vv.dispatch('scroll')
    expect(frames.pendingFrames()).toBe(1)
    expect(order).toEqual([])

    frames.runFrames()
    expect(order).toEqual([
      'phase1-inset',
      'phase2-overlay:true',
    ])
  })

  it('notifies subscribers on inset-only updates only when the inset changed', () => {
    const vv = createMockVisualViewport(800, 0)
    const frames = createManualFrames()
    vi.stubGlobal('visualViewport', vv)
    vi.stubGlobal('requestAnimationFrame', frames.requestFrame)
    vi.stubGlobal('cancelAnimationFrame', frames.cancelFrame)
    onTestFinished(() => {
      vi.unstubAllGlobals()
    })

    let changed = false
    const unregUpdater = registerVisualViewportInsetUpdater(() => changed)
    const subscriber = vi.fn()
    const unsubOverlay = subscribeVisualViewport(subscriber)
    onTestFinished(() => {
      unsubOverlay()
      unregUpdater()
    })

    scheduleVisualViewportUpdate('inset')
    frames.runFrames()
    expect(subscriber).not.toHaveBeenCalled()

    changed = true
    scheduleVisualViewportUpdate('inset')
    frames.runFrames()
    expect(subscriber).toHaveBeenCalledWith({ resized: true })
  })

  it('detaches native listeners and aborts pending frames when the last subscriber leaves', () => {
    const vv = createMockVisualViewport(800, 0)
    const frames = createManualFrames()
    vi.stubGlobal('visualViewport', vv)
    vi.stubGlobal('requestAnimationFrame', frames.requestFrame)
    vi.stubGlobal('cancelAnimationFrame', frames.cancelFrame)
    onTestFinished(() => {
      vi.unstubAllGlobals()
    })

    const subscriber = vi.fn()
    const unsub = subscribeVisualViewport(subscriber)
    expect(vv.listenerCount('resize')).toBe(1)
    expect(vv.listenerCount('scroll')).toBe(1)

    vv.dispatch('scroll')
    expect(frames.pendingFrames()).toBe(1)

    unsub()
    expect(vv.listenerCount('resize')).toBe(0)
    expect(vv.listenerCount('scroll')).toBe(0)
    expect(frames.pendingFrames()).toBe(0)
    frames.runFrames()
    expect(subscriber).not.toHaveBeenCalled()
  })
})
