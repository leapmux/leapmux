import type { ScrollContext } from './useChatScroll'
import { describe, expect, it, vi } from 'vitest'
import { createScrollInput } from './chatScrollInput'

function setup() {
  const el = {
    scrollTop: 200,
    clientHeight: 500,
    scrollHeight: 2_000,
  } as HTMLDivElement
  const writes: number[] = []
  const setAtBottom = vi.fn()
  const ctx: ScrollContext = {
    getEl: () => el,
    virt: {} as ScrollContext['virt'],
    atBottom: () => false,
    setAtBottom,
    isAtBottom: () => false,
    isFollowing: () => false,
    isAnimating: () => false,
    followTail: vi.fn(),
    refreshViewport: vi.fn(),
    writeScrollTop: (top) => {
      writes.push(top)
      el.scrollTop = top
    },
    syncVelocityToProgrammatic: vi.fn(),
    setAnchor: vi.fn(),
  }
  const extras = {
    captureAnchor: vi.fn(),
    captureTopAnchor: vi.fn(),
    checkAtBottom: vi.fn(),
    forceScrollToBottom: vi.fn(),
    cancelScrollAnimation: vi.fn(),
    cancelPendingScroll: vi.fn(),
    tryLoadOlderOnExplicitTopIntent: vi.fn(),
    tryLoadNewerOnExplicitBottomIntent: vi.fn(),
    setLastScrollDir: vi.fn(),
    setDiscretePageTarget: vi.fn(),
    hasOlderMessages: () => false,
    onJumpToOldest: undefined,
  }
  return { ctx, el, extras, input: createScrollInput(ctx, extras), setAtBottom, writes }
}

describe('createscrollinput', () => {
  it('pins Home jumps to the viewport-top anchor', () => {
    const { extras, input, setAtBottom, writes } = setup()
    const event = new KeyboardEvent('keydown', { key: 'Home', cancelable: true })

    input.handleKeyDown(event)

    expect(event.defaultPrevented).toBe(true)
    expect(extras.setLastScrollDir).toHaveBeenCalledWith('older')
    expect(writes).toEqual([0])
    expect(extras.captureTopAnchor).toHaveBeenCalledTimes(1)
    expect(extras.captureAnchor).not.toHaveBeenCalled()
    expect(setAtBottom).toHaveBeenCalledWith(false)
  })
})
