import { createSignal, onCleanup } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { flush, inRoot, untilTrue } from './async'

describe('inRoot', () => {
  it('resolves after the body and disposes the root', async () => {
    const events: string[] = []

    await inRoot(async () => {
      onCleanup(() => events.push('disposed'))
      await flush()
      events.push('body done')
    })

    expect(events).toEqual(['body done', 'disposed'])
  })

  it('rejects with the error of the body and disposes the root', async () => {
    const failure = new Error('assertion failed in the body')
    let disposed = false

    await expect(inRoot(async () => {
      onCleanup(() => {
        disposed = true
      })
      await flush()
      throw failure
    })).rejects.toBe(failure)

    expect(disposed).toBe(true)
  })

  it('rejects with an Error that states a thrown value that is not an Error', async () => {
    await expect(inRoot(async () => {
      await flush()
      // eslint-disable-next-line no-throw-literal -- the case under test is a non-Error throw
      throw 'not an error'
    })).rejects.toThrow(new Error('not an error'))
  })
})

describe('untilTrue', () => {
  it('resolves at once when the condition already holds', async () => {
    await expect(untilTrue(() => true)).resolves.toBeUndefined()
  })

  it('stays pending while the condition is false', async () => {
    const [ready] = createSignal(false)
    let settled = false
    void untilTrue(() => ready()).then(() => {
      settled = true
    })

    await flush()

    expect(settled).toBe(false)
  })

  it('resolves when a signal that the condition reads changes', async () => {
    const [ready, setReady] = createSignal(false)
    let settled = false
    const waiting = untilTrue(() => ready()).then(() => {
      settled = true
    })
    await flush()
    expect(settled).toBe(false)

    setReady(true)
    await waiting

    expect(settled).toBe(true)
  })

  it('stops reading the condition once it holds', async () => {
    const [count, setCount] = createSignal(0)
    let reads = 0
    const waiting = untilTrue(() => {
      reads++
      return count() > 0
    })

    setCount(1)
    await waiting
    const readsWhenSettled = reads
    setCount(2)
    setCount(3)

    expect(reads).toBe(readsWhenSettled)
  })
})
