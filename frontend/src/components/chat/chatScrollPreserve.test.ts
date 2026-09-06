import { afterEach, describe, expect, it, vi } from 'vitest'
import { registerProgrammaticScrollWriter, withChatScrollPreserved } from './chatScrollPreserve'

/**
 * A stand-in for one chat tile's scroll container.
 *
 * happy-dom does not lay anything out, so `scrollTop` and `scrollHeight` are
 * defined here as plain writable properties. That is exactly what the repair
 * reads and writes, and the layout it reacts to is the browser's, which no
 * unit test can produce.
 */
function addContainer(scrollTop: number, scrollHeight: number): HTMLDivElement {
  const el = document.createElement('div')
  el.setAttribute('data-chat-scroll-container', 'true')
  Object.defineProperty(el, 'scrollHeight', { value: scrollHeight, writable: true })
  el.scrollTop = scrollTop
  document.body.appendChild(el)
  return el
}

/** Run past the two nested `requestAnimationFrame` hops the restore uses. */
async function flushFrames() {
  await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve(null))))
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('withChatScrollPreserved', () => {
  it('puts back a scroll position that the work clamped to 0', async () => {
    const el = addContainer(400, 5000)

    await withChatScrollPreserved(async () => {
      el.scrollTop = 0
    })
    await flushFrames()

    expect(el.scrollTop).toBe(400)
  })

  // Every chat container, the visible tab plus any hidden one, so each keeps
  // its own position. A `querySelector` would catch only the first DOM match,
  // which is often the hidden tab.
  it('restores every container, not only the first', async () => {
    const visible = addContainer(120, 3000)
    const hidden = addContainer(900, 4000)

    await withChatScrollPreserved(async () => {
      visible.scrollTop = 0
      hidden.scrollTop = 0
    })
    await flushFrames()

    expect(visible.scrollTop).toBe(120)
    expect(hidden.scrollTop).toBe(900)
  })

  it('leaves a container the work did not move', async () => {
    const el = addContainer(250, 3000)
    const spy = vi.spyOn(el, 'scrollTop', 'set')

    await withChatScrollPreserved(async () => {})
    await flushFrames()

    expect(spy).not.toHaveBeenCalled()
    expect(el.scrollTop).toBe(250)
  })

  // A big height change means the content really reloaded, and the position
  // that belonged to the old content is not the one to put back.
  it('does not fight a real content reload', async () => {
    const el = addContainer(400, 5000)

    await withChatScrollPreserved(async () => {
      Object.defineProperty(el, 'scrollHeight', { value: 12000, writable: true })
      el.scrollTop = 0
    })
    await flushFrames()

    expect(el.scrollTop).toBe(0)
  })

  it('still restores across a height change inside the tolerance', async () => {
    const el = addContainer(400, 5000)

    await withChatScrollPreserved(async () => {
      Object.defineProperty(el, 'scrollHeight', { value: 5100, writable: true })
      el.scrollTop = 0
    })
    await flushFrames()

    expect(el.scrollTop).toBe(400)
  })

  it('skips a container the work unmounted', async () => {
    const el = addContainer(400, 5000)

    await withChatScrollPreserved(async () => {
      el.scrollTop = 0
      el.remove()
    })
    await flushFrames()

    // Nothing to assert on the element itself; the point is that reaching a
    // detached node must not throw and must not resurrect its position.
    expect(el.isConnected).toBe(false)
    expect(el.scrollTop).toBe(0)
  })

  // The wrapper repairs the DOM and decides nothing about the failure, so the
  // caller keeps its own rejection -- and the restore still runs.
  it('restores after the work throws, and rethrows', async () => {
    const el = addContainer(400, 5000)
    const boom = new Error('probe failed')

    await expect(withChatScrollPreserved(async () => {
      el.scrollTop = 0
      throw boom
    })).rejects.toBe(boom)
    await flushFrames()

    expect(el.scrollTop).toBe(400)
  })

  it('does nothing when no chat is on screen', async () => {
    await expect(withChatScrollPreserved(async () => {})).resolves.toBeUndefined()
  })

  // A restore is not a user gesture. `useChatScroll` can only know that if the
  // write goes through its own programmatic path, which records the landing
  // pixel so the resulting scroll event is recognized as the app's own; a bare
  // assignment was measured as a fling instead.
  it('writes through the controller that registered the element', async () => {
    const el = addContainer(400, 5000)
    const write = vi.fn((top: number) => {
      el.scrollTop = top
    })
    registerProgrammaticScrollWriter(el, write)

    await withChatScrollPreserved(async () => {
      el.scrollTop = 0
    })
    await flushFrames()

    expect(write).toHaveBeenCalledTimes(1)
    expect(write.mock.calls[0]![0]).toBe(400)
    expect(el.scrollTop).toBe(400)
  })

  it('does not call the controller when the restore is not needed', async () => {
    const el = addContainer(250, 3000)
    const write = vi.fn()
    registerProgrammaticScrollWriter(el, write)

    await withChatScrollPreserved(async () => {})
    await flushFrames()

    expect(write).not.toHaveBeenCalled()
  })

  // An element whose controller detached, and one that never had a controller,
  // both still get their position back -- an unmarked write beats leaving the
  // chat clamped to 0.
  it('falls back to a direct write once the element is forgotten', async () => {
    const el = addContainer(400, 5000)
    const write = vi.fn()
    registerProgrammaticScrollWriter(el, write)
    registerProgrammaticScrollWriter(el, undefined)

    await withChatScrollPreserved(async () => {
      el.scrollTop = 0
    })
    await flushFrames()

    expect(write).not.toHaveBeenCalled()
    expect(el.scrollTop).toBe(400)
  })
})
