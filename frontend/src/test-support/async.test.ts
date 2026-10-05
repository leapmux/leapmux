import { createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { flush, untilTrue } from './async'

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
