import { afterEach, describe, expect, it, vi } from 'vitest'
import { deferred, settle } from '~/test-support/async'
import { createScrollInput, isListScrollKey } from './chatScrollInput'
import { innerScrollerTarget, makeFakeScrollDiv, makeScrollContext } from './useChatScroll.testkit'

const platform = vi.hoisted(() => ({ mac: false }))
vi.mock('~/lib/shortcuts/platform', () => ({
  isMac: () => platform.mac,
}))

afterEach(() => {
  platform.mac = false
  document.body.replaceChildren()
})

/**
 * The input layer over a fake element with `clientHeight` 500 and `scrollHeight` 5000, and
 * spies on every context primitive that acts and on every extra. `calls` records the order of
 * the calls, so `toEqual([])` asserts that an input did nothing.
 */
function setup(opts: {
  scrollTop?: number
  clientHeight?: number
  scrollHeight?: number
  hasOlder?: boolean
  hasNewer?: boolean
  onJumpToOldest?: () => Promise<void>
} = {}) {
  const div = makeFakeScrollDiv()
  div.setClientHeight(opts.clientHeight ?? 500)
  div.setScrollHeight(opts.scrollHeight ?? 5000)
  div.setScrollTop(opts.scrollTop ?? 2000)
  const calls: string[] = []
  const spy = (name: string) => vi.fn(() => {
    calls.push(name)
  })
  const placeAt = vi.fn((_resolve: () => number | null): boolean | null => {
    calls.push('placeAt')
    return true
  })
  // Writes as the hook's writer does; the record is the hook's own concern.
  const writeOwnScrollTop = vi.fn((top: number, _opts: { refresh: boolean }) => {
    calls.push('writeOwnScrollTop')
    div.el.scrollTop = top
  })
  const ctx = makeScrollContext({
    getEl: () => div.el,
    hasNewerMessages: () => opts.hasNewer ?? false,
    followTail: spy('followTail'),
    leaveFollow: spy('leaveFollow'),
    checkPosition: spy('checkPosition'),
    cancelScrollAnimation: spy('cancelScrollAnimation'),
    retakeScrollControl: spy('retakeScrollControl'),
    rearmScrollBufferFiller: spy('rearmScrollBufferFiller'),
    rebase: spy('rebase'),
    placeAt,
    writeOwnScrollTop,
  })
  const extras = {
    forceScrollToBottom: spy('forceScrollToBottom'),
    cancelPendingScroll: spy('cancelPendingScroll'),
    cancelPendingSeek: spy('cancelPendingSeek'),
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
    writeOwnScrollTop,
    input: createScrollInput(ctx, extras),
  }
}

const key = (k: string, init: KeyboardEventInit = {}) => new KeyboardEvent('keydown', { key: k, cancelable: true, ...init })
const wheel = (deltaY: number, deltaX = 0) => ({ deltaY, deltaX, ctrlKey: false }) as WheelEvent

/** Dispatch `event` at `target`, where it bubbles to `handler` on the list `div`, as in ChatView. */
function dispatchInside<E extends Event>(div: ReturnType<typeof makeFakeScrollDiv>, target: Element, event: E, handler: (event: E) => void): void {
  const listener = (e: Event) => handler(e as E)
  div.el.addEventListener(event.type, listener)
  target.dispatchEvent(event)
  div.el.removeEventListener(event.type, listener)
}

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

  it('re-checks the position and places nothing when the jump to the oldest page fails', async () => {
    const jump = deferred<void>()
    const onJumpToOldest = vi.fn(() => jump.promise)
    const { input, calls, placeAt } = setup({ hasOlder: true, onJumpToOldest })
    input.handleKeyDown(key('Home'))

    jump.reject(new Error('the oldest page did not load'))
    await settle()

    expect(placeAt).not.toHaveBeenCalled()
    expect(calls).toContain('checkPosition')
  })

  it('rebases, writes, and retakes control for a page that moves', () => {
    const { input, calls, div, writeOwnScrollTop } = setup({ scrollTop: 2000 })
    input.pageScroll(1)
    expect(calls).toEqual(['rebase', 'writeOwnScrollTop', 'retakeScrollControl'])
    // delta = max(500 - 48, 250) = 452.
    expect(writeOwnScrollTop).toHaveBeenCalledWith(2452, { refresh: true })
    expect(div.el.scrollTop).toBe(2452)
  })

  it('leaves follow mode when PageUp moves the view', () => {
    const { input, calls } = setup({ scrollTop: 2000 })
    input.pageScroll(-1)
    expect(calls.filter(call => call === 'leaveFollow')).toHaveLength(1)
    // After the write, so the leave records the anchor of the new position.
    expect(calls.indexOf('writeOwnScrollTop')).toBeLessThan(calls.indexOf('leaveFollow'))
  })

  it('keeps follow mode when PageUp cannot move', () => {
    const { input, calls } = setup({ scrollTop: 0 })
    input.pageScroll(-1)
    expect(calls).not.toContain('leaveFollow')
    expect(calls).toContain('tryLoadOlder')
    expect(calls).not.toContain('retakeScrollControl')
  })

  it('calls the newer loader for PageDown at the bottom', () => {
    const { input, calls } = setup({ scrollTop: 4500 })
    input.pageScroll(1)
    expect(calls).toContain('tryLoadNewer')
    expect(calls).not.toContain('retakeScrollControl')
  })

  it('records a page that moves less than the edge tolerance', () => {
    // The maximum position is 4500.5, so a PageDown at 4500 moves half a pixel.
    const { input, calls, div, writeOwnScrollTop } = setup({ scrollTop: 4500, scrollHeight: 5000.5 })
    input.pageScroll(1)
    expect(div.el.scrollTop).toBe(4500.5)
    // The page writes only through the writer, which records the write, so the `scroll`
    // event of the write reads as no move.
    expect(writeOwnScrollTop).toHaveBeenCalledOnce()
    expect(div.scrollTopWrites()).toBe(1)
    // Half a pixel is no page: the view sits at the edge.
    expect(calls).toContain('tryLoadNewer')
    expect(calls).not.toContain('retakeScrollControl')
    expect(calls).not.toContain('leaveFollow')
  })

  it('ends the request for the tail when PageDown leaves the view above the loaded bottom of a paged-away window', () => {
    const { input, calls, div } = setup({ scrollTop: 2000, hasNewer: true })
    input.pageScroll(1)
    expect(div.el.scrollTop).toBe(2452)
    expect(calls.filter(call => call === 'leaveFollow')).toHaveLength(1)
  })

  it('keeps the request for the tail when PageDown reaches the loaded bottom of a paged-away window', () => {
    const { input, calls, div } = setup({ scrollTop: 4200, hasNewer: true })
    input.pageScroll(1)
    expect(div.el.scrollTop).toBe(4500)
    expect(calls).not.toContain('leaveFollow')
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

  it('ignores an upward wheel that a scroller inside the list takes', () => {
    const { input, div, calls, extras } = setup({ scrollTop: 2000 })
    const target = innerScrollerTarget(div.el, { scrollTop: 200 })
    dispatchInside(div, target, new WheelEvent('wheel', { deltaY: -10, bubbles: true }), input.handleWheel)
    // The list does not move, so the wheel is no input of the list: no follow end, no seek
    // cancel, no direction, and no edge load.
    expect(calls).toEqual([])
    expect(extras.setLastScrollDir).not.toHaveBeenCalled()
  })

  it('ignores a downward wheel that a scroller inside the list takes', () => {
    const { input, div, calls, extras } = setup({ scrollTop: 2000 })
    const target = innerScrollerTarget(div.el, { scrollTop: 200 })
    dispatchInside(div, target, new WheelEvent('wheel', { deltaY: 10, bubbles: true }), input.handleWheel)
    expect(calls).toEqual([])
    expect(extras.setLastScrollDir).not.toHaveBeenCalled()
  })

  it('ignores an upward wheel that a scroller with overflow-y scroll takes', () => {
    const { input, div, calls, extras } = setup({ scrollTop: 2000 })
    const target = innerScrollerTarget(div.el, { scrollTop: 200, overflowY: 'scroll' })
    dispatchInside(div, target, new WheelEvent('wheel', { deltaY: -10, bubbles: true }), input.handleWheel)
    expect(calls).toEqual([])
    expect(extras.setLastScrollDir).not.toHaveBeenCalled()
  })

  it('steps newer on a downward wheel inside a scroller at its own bottom', () => {
    const { input, div, calls, extras } = setup({ scrollTop: 2000 })
    // The inner scroller spans 1000px in a 300px box, so 700 is its bottom.
    const target = innerScrollerTarget(div.el, { scrollTop: 700 })
    dispatchInside(div, target, new WheelEvent('wheel', { deltaY: 10, bubbles: true }), input.handleWheel)
    expect(calls).toEqual(['cancelPendingSeek', 'tryLoadNewer'])
    expect(extras.setLastScrollDir).toHaveBeenCalledWith('newer')
  })

  it('leaves follow mode on an upward wheel inside a scroller at its own top', () => {
    const { input, div, calls } = setup({ scrollTop: 2000 })
    const target = innerScrollerTarget(div.el, { scrollTop: 0 })
    dispatchInside(div, target, new WheelEvent('wheel', { deltaY: -10, bubbles: true }), input.handleWheel)
    expect(calls).toContain('leaveFollow')
  })

  it('leaves follow mode on an upward wheel inside an element that does not scroll', () => {
    const { input, div, calls } = setup({ scrollTop: 2000 })
    // A position above 0 with `overflow-y: hidden`: the wheel cannot move that element.
    const target = innerScrollerTarget(div.el, { scrollTop: 200, overflowY: 'hidden' })
    dispatchInside(div, target, new WheelEvent('wheel', { deltaY: -10, bubbles: true }), input.handleWheel)
    expect(calls).toContain('leaveFollow')
  })

  it('ignores a scroller outside the list', () => {
    const { input, div, calls } = setup({ scrollTop: 2000 })
    document.body.appendChild(div.el)
    const outside = innerScrollerTarget(document.body, { scrollTop: 200 })
    input.handleWheel({
      deltaY: -10,
      deltaX: 0,
      ctrlKey: false,
      target: outside,
    } as unknown as WheelEvent)
    expect(calls).toContain('leaveFollow')
  })

  it('ignores ArrowUp from inside a scroller that can move up', () => {
    const { input, div, calls, extras } = setup({ scrollTop: 2000 })
    const target = innerScrollerTarget(div.el, { scrollTop: 200 })
    dispatchInside(div, target, key('ArrowUp', { bubbles: true }), input.handleKeyDown)
    expect(calls).toEqual([])
    expect(extras.setLastScrollDir).not.toHaveBeenCalled()
  })

  it('ignores a scroll key from a text field inside the list', () => {
    const { input, div, calls, extras } = setup({ scrollTop: 2000 })
    const field = document.createElement('textarea')
    div.el.appendChild(field)
    document.body.appendChild(div.el)
    for (const k of [
      'Home',
      'End',
      'PageUp',
      'PageDown',
      'ArrowUp',
      'ArrowDown',
      ' ',
    ]) {
      const event = key(k, { bubbles: true })
      dispatchInside(div, field, event, input.handleKeyDown)
      expect(event.defaultPrevented).toBe(false)
    }
    expect(calls).toEqual([])
    expect(extras.setLastScrollDir).not.toHaveBeenCalled()
  })

  it('leaves follow mode on Shift+Space with scroll room', () => {
    const { input, div, calls, extras } = setup({ scrollTop: 2000 })
    const event = key(' ', { shiftKey: true })
    input.handleKeyDown(event)
    div.setScrollTop(0)
    input.handleKeyDown(key(' ', { shiftKey: true }))
    expect(calls.filter(call => call === 'leaveFollow')).toHaveLength(1)
    expect(extras.setLastScrollDir).toHaveBeenCalledWith('older')
    expect(extras.tryLoadOlderOnExplicitTopIntent).toHaveBeenCalledTimes(2)
    // The browser scrolls the page.
    expect(event.defaultPrevented).toBe(false)
  })

  it('steps newer on Space and keeps follow mode', () => {
    const { input, calls, extras } = setup({ scrollTop: 2000 })
    const event = key(' ')
    input.handleKeyDown(event)
    expect(calls).toEqual(['cancelPendingSeek', 'tryLoadNewer'])
    expect(extras.setLastScrollDir).toHaveBeenCalledWith('newer')
    // The browser scrolls the page.
    expect(event.defaultPrevented).toBe(false)
  })

  it('leaves follow mode on Option+ArrowUp and Cmd+ArrowUp on macOS', () => {
    platform.mac = true
    const { input, calls, extras } = setup({ scrollTop: 2000 })
    const option = key('ArrowUp', { altKey: true })
    input.handleKeyDown(option)
    input.handleKeyDown(key('ArrowUp', { metaKey: true }))
    expect(calls.filter(call => call === 'leaveFollow')).toHaveLength(2)
    expect(extras.setLastScrollDir).toHaveBeenCalledWith('older')
    expect(option.defaultPrevented).toBe(false)
  })

  it('steps newer on Option+ArrowDown and Cmd+ArrowDown on macOS', () => {
    platform.mac = true
    const { input, calls, extras } = setup({ scrollTop: 2000 })
    input.handleKeyDown(key('ArrowDown', { altKey: true }))
    input.handleKeyDown(key('ArrowDown', { metaKey: true }))
    expect(calls).toEqual([
      'cancelPendingSeek',
      'tryLoadNewer',
      'cancelPendingSeek',
      'tryLoadNewer',
    ])
    expect(extras.setLastScrollDir).toHaveBeenCalledWith('newer')
  })

  it('ignores Option+ArrowUp outside macOS, and Option or Cmd with another key', () => {
    const { input, calls, extras } = setup({ scrollTop: 2000 })
    input.handleKeyDown(key('ArrowUp', { altKey: true }))
    input.handleKeyDown(key('ArrowDown', { metaKey: true }))
    platform.mac = true
    input.handleKeyDown(key('PageUp', { metaKey: true }))
    input.handleKeyDown(key('Home', { altKey: true }))
    input.handleKeyDown(key('ArrowUp', { ctrlKey: true }))
    expect(calls).toEqual([])
    expect(extras.setLastScrollDir).not.toHaveBeenCalled()
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

describe('isListScrollKey', () => {
  it('accepts each scroll key with no modifier or with Shift', () => {
    for (const k of [
      'ArrowUp',
      'ArrowDown',
      'PageUp',
      'PageDown',
      'Home',
      'End',
      ' ',
    ]) {
      expect(isListScrollKey(key(k))).toBe(true)
      expect(isListScrollKey(key(k, { shiftKey: true }))).toBe(true)
    }
  })

  it('rejects a key that does not scroll', () => {
    expect(isListScrollKey(key('a'))).toBe(false)
    expect(isListScrollKey(key('Enter'))).toBe(false)
  })

  it('accepts Option or Cmd with an arrow on macOS only', () => {
    expect(isListScrollKey(key('ArrowUp', { altKey: true }))).toBe(false)
    expect(isListScrollKey(key('ArrowDown', { metaKey: true }))).toBe(false)
    platform.mac = true
    expect(isListScrollKey(key('ArrowUp', { altKey: true }))).toBe(true)
    expect(isListScrollKey(key('ArrowDown', { metaKey: true }))).toBe(true)
    expect(isListScrollKey(key('PageDown', { metaKey: true }))).toBe(false)
    expect(isListScrollKey(key('End', { altKey: true }))).toBe(false)
  })

  it('rejects every key with Ctrl', () => {
    platform.mac = true
    expect(isListScrollKey(key('ArrowUp', { ctrlKey: true }))).toBe(false)
    expect(isListScrollKey(key('End', { ctrlKey: true }))).toBe(false)
  })

  it('rejects a key from a text field', () => {
    for (const field of [
      document.createElement('input'),
      document.createElement('textarea'),
      document.createElement('select'),
    ]) {
      document.body.appendChild(field)
      const event = key('Home', { bubbles: true })
      let result: boolean | undefined
      field.addEventListener('keydown', (e) => {
        result = isListScrollKey(e as KeyboardEvent)
      })
      field.dispatchEvent(event)
      expect(result).toBe(false)
    }
  })

  it('rejects a key from an element that edits its content', () => {
    const editor = document.createElement('div')
    editor.setAttribute('contenteditable', 'true')
    // jsdom implements neither contentEditable nor isContentEditable, so the test sets the
    // property that a browser derives from the attribute.
    Object.defineProperty(editor, 'isContentEditable', { value: true })
    document.body.appendChild(editor)
    let result: boolean | undefined
    editor.addEventListener('keydown', (e) => {
      result = isListScrollKey(e as KeyboardEvent)
    })
    editor.dispatchEvent(key('ArrowDown', { bubbles: true }))
    expect(result).toBe(false)
  })
})
