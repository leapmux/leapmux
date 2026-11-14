import type { ChatScrollController } from './chatScrollPreserve'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { registerChatScrollController, withChatScrollPreserved } from './chatScrollPreserve'

/** A chat scroll container, as ChatView marks it. */
function addContainer(): HTMLDivElement {
  const el = document.createElement('div')
  el.setAttribute('data-chat-scroll-container', 'true')
  document.body.appendChild(el)
  return el
}

/** A controller whose snapshot is `snapshot`, with a spy on its restore. */
function fakeController<T>(snapshot: T | undefined) {
  const controller = {
    snapshot: vi.fn(() => snapshot),
    restore: vi.fn((_snapshot: T) => {}),
  } satisfies ChatScrollController<T>
  return controller
}

/** Run past the two nested `requestAnimationFrame` hops the restore uses. */
async function flushFrames() {
  await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve(null))))
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('withChatScrollPreserved', () => {
  it('calls restore with the snapshot of each controller', async () => {
    const first = addContainer()
    const second = addContainer()
    const firstController = fakeController({ tile: 'first' })
    const secondController = fakeController({ tile: 'second' })
    registerChatScrollController(first, firstController)
    registerChatScrollController(second, secondController)

    await withChatScrollPreserved(async () => {})
    await flushFrames()

    expect(firstController.restore).toHaveBeenCalledOnce()
    expect(firstController.restore).toHaveBeenCalledWith({ tile: 'first' })
    expect(secondController.restore).toHaveBeenCalledOnce()
    expect(secondController.restore).toHaveBeenCalledWith({ tile: 'second' })
  })

  it('takes the snapshot before the work runs', async () => {
    const el = addContainer()
    let position = 400
    const controller = {
      snapshot: vi.fn(() => position),
      restore: vi.fn((_snapshot: number) => {}),
    }
    registerChatScrollController(el, controller)

    await withChatScrollPreserved(async () => {
      position = 0
    })
    await flushFrames()

    expect(controller.restore).toHaveBeenCalledWith(400)
  })

  it('skips a container without a controller', async () => {
    const el = addContainer()
    el.scrollTop = 0

    await expect(withChatScrollPreserved(async () => {})).resolves.toBeUndefined()
    await flushFrames()

    expect(el.scrollTop).toBe(0)
  })

  it('skips a controller whose snapshot is undefined', async () => {
    const el = addContainer()
    const controller = fakeController<number>(undefined)
    registerChatScrollController(el, controller)

    await withChatScrollPreserved(async () => {})
    await flushFrames()

    expect(controller.snapshot).toHaveBeenCalledOnce()
    expect(controller.restore).not.toHaveBeenCalled()
  })

  it('skips a container that the work unmounted', async () => {
    const el = addContainer()
    const controller = fakeController(1)
    registerChatScrollController(el, controller)

    await withChatScrollPreserved(async () => {
      el.remove()
    })
    await flushFrames()

    expect(controller.restore).not.toHaveBeenCalled()
  })

  it('restores after the work throws, and rethrows', async () => {
    const el = addContainer()
    const controller = fakeController(1)
    registerChatScrollController(el, controller)
    const failure = new Error('the refresh failed')

    await expect(withChatScrollPreserved(async () => {
      throw failure
    })).rejects.toBe(failure)
    await flushFrames()

    expect(controller.restore).toHaveBeenCalledOnce()
  })

  it('does nothing when no chat is on screen', async () => {
    await expect(withChatScrollPreserved(async () => {})).resolves.toBeUndefined()
  })

  it('forgets a controller that is registered as undefined', async () => {
    const el = addContainer()
    const controller = fakeController(1)
    registerChatScrollController(el, controller)
    registerChatScrollController(el, undefined)

    await withChatScrollPreserved(async () => {})
    await flushFrames()

    expect(controller.snapshot).not.toHaveBeenCalled()
    expect(controller.restore).not.toHaveBeenCalled()
  })

  it('ignores an element that is not a chat scroll container', async () => {
    const el = document.createElement('div')
    document.body.appendChild(el)
    const controller = fakeController(1)
    registerChatScrollController(el, controller)

    await withChatScrollPreserved(async () => {})
    await flushFrames()

    expect(controller.snapshot).not.toHaveBeenCalled()
  })
})
