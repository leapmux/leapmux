import { describe, expect, it, vi } from 'vitest'
import { createScrollInput } from './chatScrollInput'
import { makeFakeScrollDiv, makeScrollContext } from './useChatScroll.testkit'

/**
 * The input layer over a fake element with `clientHeight` 500 and `scrollHeight` 5000, and
 * spies on every context primitive that acts and on every extra. `calls` records the order of
 * the calls, so `toEqual([])` asserts that an input did nothing.
 */
function setup(opts: { scrollTop?: number, clientHeight?: number, hasOlder?: boolean, onJumpToOldest?: () => Promise<void> } = {}) {
  const div = makeFakeScrollDiv()
  div.setClientHeight(opts.clientHeight ?? 500)
  div.setScrollHeight(5000)
  div.setScrollTop(opts.scrollTop ?? 2000)
  const calls: string[] = []
  const spy = (name: string) => vi.fn(() => {
    calls.push(name)
  })
  const placeAt = vi.fn((_resolve: () => number | null): boolean | null => {
    calls.push('placeAt')
    return true
  })
  const ctx = makeScrollContext({
    getEl: () => div.el,
    setAtBottom: spy('setAtBottom'),
    refreshViewport: spy('refreshViewport'),
    followTail: spy('followTail'),
    leaveFollow: spy('leaveFollow'),
    rebase: spy('rebase'),
    notePosition: spy('notePosition'),
    syncAfterWrite: spy('syncAfterWrite'),
    placeAt,
  })
  const extras = {
    checkAtBottom: spy('checkAtBottom'),
    forceScrollToBottom: spy('forceScrollToBottom'),
    cancelScrollAnimation: spy('cancelScrollAnimation'),
    cancelPendingScroll: spy('cancelPendingScroll'),
    cancelPendingSeek: spy('cancelPendingSeek'),
    retakeScrollControl: spy('retakeScrollControl'),
    tryLoadOlderOnExplicitTopIntent: spy('tryLoadOlder'),
    tryLoadNewerOnExplicitBottomIntent: spy('tryLoadNewer'),
    setLastScrollDir: vi.fn(),
    hasOlderMessages: () => opts.hasOlder ?? false,
    onJumpToOldest: opts.onJumpToOldest,
  }
  return {
    div,
    ctx,
    extras,
    calls,
    placeAt,
    input: createScrollInput(ctx, extras),
  }
}

const key = (k: string, init: KeyboardEventInit = {}) => new KeyboardEvent('keydown', { key: k, cancelable: true, ...init })
const wheel = (deltaY: number, deltaX = 0) => ({ deltaY, deltaX, ctrlKey: false }) as WheelEvent

describe('createScrollInput', () => {
  it('calls placeAt with a resolver of 0 for Home', () => {
    const { input, placeAt, extras } = setup()
    const event = key('Home')
    input.handleKeyDown(event)
    expect(event.defaultPrevented).toBe(true)
    expect(extras.setLastScrollDir).toHaveBeenCalledWith('older')
    expect(placeAt).toHaveBeenCalledOnce()
    expect(placeAt.mock.calls[0]![0]()).toBe(0)
  })

  it('jumps to the oldest page before it places the view at the top', async () => {
    let resolveJump!: () => void
    const onJumpToOldest = vi.fn(() => new Promise<void>((resolve) => {
      resolveJump = resolve
    }))
    const { input, placeAt } = setup({ hasOlder: true, onJumpToOldest })
    input.handleKeyDown(key('Home'))
    expect(onJumpToOldest).toHaveBeenCalledOnce()
    expect(placeAt).not.toHaveBeenCalled()
    resolveJump()
    await Promise.resolve()
    await Promise.resolve()
    expect(placeAt).toHaveBeenCalledOnce()
    expect(placeAt.mock.calls[0]![0]()).toBe(0)
  })

  it('rebases, scrolls, and syncs for a page that moves', () => {
    const { input, calls, div } = setup({ scrollTop: 2000 })
    input.pageScroll(1)
    expect(calls).toEqual(['rebase', 'retakeScrollControl', 'syncAfterWrite'])
    // delta = max(500 - 48, 250) = 452.
    expect(div.el.scrollTop).toBe(2452)
  })

  it('leaves follow mode when PageUp moves the view', () => {
    const { input, calls } = setup({ scrollTop: 2000 })
    input.pageScroll(-1)
    expect(calls.filter(call => call === 'leaveFollow')).toHaveLength(1)
    expect(calls.indexOf('leaveFollow')).toBeLessThan(calls.indexOf('syncAfterWrite'))
  })

  it('keeps follow mode when PageUp cannot move', () => {
    const { input, calls } = setup({ scrollTop: 0 })
    input.pageScroll(-1)
    expect(calls).not.toContain('leaveFollow')
    expect(calls).toContain('tryLoadOlder')
    expect(calls).not.toContain('syncAfterWrite')
  })

  it('calls the newer loader for PageDown at the bottom', () => {
    const { input, calls } = setup({ scrollTop: 4500 })
    input.pageScroll(1)
    expect(calls).toContain('tryLoadNewer')
    expect(calls).not.toContain('syncAfterWrite')
  })

  it('does nothing for a hidden element', () => {
    const { input, calls } = setup({ clientHeight: 0 })
    input.pageScroll(1)
    expect(calls).toEqual([])
  })

  it('leaves follow mode on an upward wheel only with scroll room', () => {
    const { input, div, calls } = setup({ scrollTop: 2000 })
    input.handleWheel(wheel(-10))
    div.setScrollTop(0)
    input.handleWheel(wheel(-10))
    expect(calls.filter(call => call === 'leaveFollow')).toHaveLength(1)
  })

  it('keeps follow mode on a downward wheel', () => {
    const { input, calls } = setup({ scrollTop: 2000 })
    input.handleWheel(wheel(10))
    expect(calls).not.toContain('leaveFollow')
  })

  it('leaves follow mode on ArrowUp with scroll room', () => {
    const { input, div, calls } = setup({ scrollTop: 2000 })
    input.handleKeyDown(key('ArrowUp'))
    div.setScrollTop(0)
    input.handleKeyDown(key('ArrowUp'))
    expect(calls.filter(call => call === 'leaveFollow')).toHaveLength(1)
  })

  it('cancels the pending seek on a vertical wheel and on each scroll key', () => {
    const { input, extras } = setup()
    input.handleWheel(wheel(10))
    for (const k of [
      'Home',
      'End',
      'PageUp',
      'PageDown',
      'ArrowUp',
      'ArrowDown',
      ' ',
    ])
      input.handleKeyDown(key(k))
    expect(extras.cancelPendingSeek).toHaveBeenCalledTimes(8)
  })

  it('cancels nothing on a horizontal wheel', () => {
    const { input, calls, extras } = setup()
    input.handleWheel(wheel(2, 30))
    expect(calls).toEqual([])
    expect(extras.setLastScrollDir).not.toHaveBeenCalled()
  })

  it('ignores a key with a modifier', () => {
    const { input, calls, extras } = setup()
    const event = key('PageDown', { ctrlKey: true })
    input.handleKeyDown(event)
    expect(calls).toEqual([])
    expect(extras.setLastScrollDir).not.toHaveBeenCalled()
    expect(event.defaultPrevented).toBe(false)
  })

  it('pages newer and prevents default when handleKeyDown receives PageDown', () => {
    const { input, extras, div } = setup({ scrollTop: 2000 })
    const event = key('PageDown')
    input.handleKeyDown(event)
    expect(event.defaultPrevented).toBe(true)
    expect(extras.setLastScrollDir).toHaveBeenCalledWith('newer')
    expect(div.el.scrollTop).toBe(2452)
  })

  it('forces scroll to the live tail when handleKeyDown receives End', () => {
    const { input, extras } = setup()
    input.handleKeyDown(key('End'))
    expect(extras.forceScrollToBottom).toHaveBeenCalledOnce()
    expect(extras.setLastScrollDir).toHaveBeenCalledWith('newer')
  })

  it('records the direction and triggers the edge-intent load by deltaY sign in handleWheel', () => {
    const { input, extras } = setup()
    input.handleWheel(wheel(-10))
    expect(extras.setLastScrollDir).toHaveBeenLastCalledWith('older')
    expect(extras.tryLoadOlderOnExplicitTopIntent).toHaveBeenCalledOnce()
    input.handleWheel(wheel(10))
    expect(extras.setLastScrollDir).toHaveBeenLastCalledWith('newer')
    expect(extras.tryLoadNewerOnExplicitBottomIntent).toHaveBeenCalledOnce()
  })

  it('cancels the pending scroll on a no-movement (0,0) event in handleWheel', () => {
    const { input, extras } = setup()
    input.handleWheel(wheel(0, 0))
    expect(extras.cancelPendingScroll).toHaveBeenCalledOnce()
    expect(extras.cancelPendingSeek).not.toHaveBeenCalled()
    expect(extras.setLastScrollDir).not.toHaveBeenCalled()
  })

  it('ignores a horizontal-dominant swipe whose small deltaY leaks in handleWheel', () => {
    const { input, extras } = setup()
    // A sideways trackpad swipe: large horizontal delta, tiny vertical leak. It must NOT
    // fire an edge-intent load or set lastScrollDir (which would mis-steer the buffer
    // filler), and is not a (0,0) momentum-cancel either.
    input.handleWheel(wheel(-3, 80))
    input.handleWheel(wheel(2, -80))
    expect(extras.setLastScrollDir).not.toHaveBeenCalled()
    expect(extras.tryLoadOlderOnExplicitTopIntent).not.toHaveBeenCalled()
    expect(extras.tryLoadNewerOnExplicitBottomIntent).not.toHaveBeenCalled()
    expect(extras.cancelPendingScroll).not.toHaveBeenCalled()
    // A vertical-dominant diagonal still counts as intent.
    input.handleWheel(wheel(-80, 3))
    expect(extras.setLastScrollDir).toHaveBeenCalledWith('older')
    expect(extras.tryLoadOlderOnExplicitTopIntent).toHaveBeenCalledOnce()
  })
})
