import { describe, expect, it, vi } from 'vitest'
import { createMockVisualViewport } from './visualViewportStub'

describe('createMockVisualViewport', () => {
  it('initializes default geometry and applies explicit height, offsetTop, and scale', () => {
    const vv = createMockVisualViewport(420, 120, 1.5)
    expect(vv.width).toBe(390)
    expect(vv.height).toBe(420)
    expect(vv.offsetTop).toBe(120)
    expect(vv.offsetLeft).toBe(0)
    expect(vv.pageTop).toBe(0)
    expect(vv.scale).toBe(1.5)
  })

  it('tracks and dispatches resize and scroll listeners independently', () => {
    const vv = createMockVisualViewport(800)
    const onResize = vi.fn()
    const onScroll = vi.fn()

    vv.addEventListener('resize', onResize)
    vv.addEventListener('resize', onResize)
    vv.addEventListener('scroll', onScroll)
    expect(vv.listenerCount('resize')).toBe(1)
    expect(vv.listenerCount('scroll')).toBe(1)

    vv.dispatch('resize')
    expect(onResize).toHaveBeenCalledTimes(1)
    expect(onScroll).not.toHaveBeenCalled()

    vv.dispatch('scroll')
    expect(onScroll).toHaveBeenCalledTimes(1)

    vv.removeEventListener('resize', onResize)
    expect(vv.listenerCount('resize')).toBe(0)
    vv.dispatch('resize')
    expect(onResize).toHaveBeenCalledTimes(1)
  })
})
