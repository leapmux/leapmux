import type { StreamSource } from './streamBuffer'
import { describe, expect, it } from 'vitest'
import { bufferStreamHandle } from './streamBuffer'

// fakeSource lets the test fire onMessage / onEnd / onError on demand.
function fakeSource<T>(): {
  source: StreamSource<T>
  fireMessage: (msg: T) => void
  fireEnd: () => void
  fireError: (err: Error) => void
} {
  let messageCb: ((msg: T) => void) | null = null
  let endCb: (() => void) | null = null
  let errorCb: ((err: Error) => void) | null = null
  return {
    source: {
      onMessage(cb) { messageCb = cb },
      onEnd(cb) { endCb = cb },
      onError(cb) { errorCb = cb },
    },
    fireMessage(msg) { messageCb?.(msg) },
    fireEnd() { endCb?.() },
    fireError(err) { errorCb?.(err) },
  }
}

describe('bufferStreamHandle', () => {
  it('flushes pre-registration messages once onEvent is called', () => {
    const fake = fakeSource<number>()
    const handle = bufferStreamHandle(fake.source)

    fake.fireMessage(1)
    fake.fireMessage(2)
    fake.fireMessage(3)

    const received: number[] = []
    handle.onEvent(n => received.push(n))

    expect(received).toEqual([1, 2, 3])
  })

  it('delivers messages directly once onEvent is wired', () => {
    const fake = fakeSource<number>()
    const handle = bufferStreamHandle(fake.source)

    const received: number[] = []
    handle.onEvent(n => received.push(n))

    fake.fireMessage(10)
    expect(received).toEqual([10])
  })

  it('flushes a pre-registration end signal exactly once', () => {
    const fake = fakeSource<number>()
    const handle = bufferStreamHandle(fake.source)

    fake.fireEnd()

    let endCount = 0
    handle.onEnd(() => {
      endCount++
    })
    expect(endCount).toBe(1)

    // Re-registering should not re-fire the buffered end.
    handle.onEnd(() => {
      endCount++
    })
    expect(endCount).toBe(1)
  })

  it('flushes a pre-registration error', () => {
    const fake = fakeSource<number>()
    const handle = bufferStreamHandle(fake.source)

    const err = new Error('transport gone')
    fake.fireError(err)

    let received: Error | null = null
    handle.onError((e) => {
      received = e
    })
    expect(received).toBe(err)
  })

  it('only flushes events once', () => {
    const fake = fakeSource<number>()
    const handle = bufferStreamHandle(fake.source)

    fake.fireMessage(1)
    fake.fireMessage(2)

    const first: number[] = []
    handle.onEvent(n => first.push(n))
    expect(first).toEqual([1, 2])

    // Re-registering must not re-flush the same buffered events.
    const second: number[] = []
    handle.onEvent(n => second.push(n))
    expect(second).toEqual([])

    // New events go to the latest callback.
    fake.fireMessage(3)
    expect(first).toEqual([1, 2])
    expect(second).toEqual([3])
  })

  it('preserves message ordering in the buffer', () => {
    const fake = fakeSource<number>()
    const handle = bufferStreamHandle(fake.source)

    fake.fireMessage(1)
    fake.fireMessage(2)
    fake.fireMessage(3)
    fake.fireMessage(4)

    const received: number[] = []
    handle.onEvent(n => received.push(n))
    expect(received).toEqual([1, 2, 3, 4])
  })
})
