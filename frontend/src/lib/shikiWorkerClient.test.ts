import { afterEach, describe, expect, it, vi } from 'vitest'

const originalWorkerDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'Worker')

function restoreWorker() {
  if (originalWorkerDescriptor) {
    Object.defineProperty(globalThis, 'Worker', originalWorkerDescriptor)
    return
  }

  Reflect.deleteProperty(globalThis, 'Worker')
}

async function importClient() {
  vi.resetModules()
  return await import('./shikiWorkerClient')
}

describe('shikiWorkerClient', () => {
  afterEach(() => {
    restoreWorker()
  })

  it('returns null when Web Workers are unavailable', async () => {
    Object.defineProperty(globalThis, 'Worker', {
      configurable: true,
      writable: true,
      value: undefined,
    })

    const { tokenizeAsync } = await importClient()

    await expect(tokenizeAsync('bash', 'echo no-worker')).resolves.toBeNull()
  })

  it('returns null when creating the worker throws synchronously', async () => {
    Object.defineProperty(globalThis, 'Worker', {
      configurable: true,
      writable: true,
      value: class ThrowingWorker {
        constructor() {
          throw new Error('blocked by CSP')
        }
      },
    })

    const { tokenizeAsync } = await importClient()

    await expect(tokenizeAsync('bash', 'echo no-worker')).resolves.toBeNull()
  })

  it('returns null and terminates the worker when postMessage throws synchronously', async () => {
    const terminateSpy = vi.fn()
    Object.defineProperty(globalThis, 'Worker', {
      configurable: true,
      writable: true,
      value: class PostThrowingWorker {
        onmessage: ((event: MessageEvent) => void) | null = null
        onerror: (() => void) | null = null
        terminate = terminateSpy

        postMessage() {
          throw new Error('worker port closed')
        }
      },
    })

    const { tokenizeAsync } = await importClient()

    await expect(tokenizeAsync('bash', 'echo post-fail')).resolves.toBeNull()
    expect(terminateSpy).toHaveBeenCalledTimes(1)
  })

  it('resolves older pending requests when a later postMessage throws synchronously', async () => {
    const terminateSpy = vi.fn()
    let posts = 0
    Object.defineProperty(globalThis, 'Worker', {
      configurable: true,
      writable: true,
      value: class FlakyWorker {
        onmessage: ((event: MessageEvent) => void) | null = null
        onerror: (() => void) | null = null
        terminate = terminateSpy

        postMessage() {
          posts++
          if (posts === 2)
            throw new Error('worker port closed')
        }
      },
    })

    const { tokenizeAsync } = await importClient()

    const first = tokenizeAsync('bash', 'echo first')
    const second = tokenizeAsync('bash', 'echo second')

    await expect(second).resolves.toBeNull()
    await expect(first).resolves.toBeNull()
    expect(terminateSpy).toHaveBeenCalledTimes(1)
  })

  it('coalesces concurrent identical requests onto one worker dispatch, then re-dispatches after it settles', async () => {
    const tokens = [[{ content: 'echo', htmlStyle: {} }]]
    const workers: Array<{
      onmessage: ((event: MessageEvent) => void) | null
      onerror: (() => void) | null
      messages: Array<{ id: number, lang: string, code: string }>
      terminate: ReturnType<typeof vi.fn>
    }> = []
    Object.defineProperty(globalThis, 'Worker', {
      configurable: true,
      writable: true,
      value: class CapturingWorker {
        onmessage: ((event: MessageEvent) => void) | null = null
        onerror: (() => void) | null = null
        messages: Array<{ id: number, lang: string, code: string }> = []
        terminate = vi.fn()

        constructor() {
          workers.push(this)
        }

        postMessage(message: { id: number, lang: string, code: string }) {
          this.messages.push(message)
        }
      },
    })
    const { tokenizeAsync } = await importClient()

    // Two concurrent requests for the SAME (lang, code) must dispatch ONCE.
    const a = tokenizeAsync('bash', 'echo dedup')
    const b = tokenizeAsync('bash', 'echo dedup')
    expect(workers[0].messages).toHaveLength(1)

    // Resolving the single dispatch resolves BOTH callers with the same tokens.
    const { id } = workers[0].messages[0]
    workers[0].onmessage?.({ data: { id, tokens } } as MessageEvent)
    await expect(a).resolves.toEqual(tokens)
    await expect(b).resolves.toEqual(tokens)

    // DIFFERENT code is never coalesced, and -- crucially -- a NULL result is NOT cached,
    // so once its in-flight entry is dropped (the `.finally` cleanup) a later request for
    // that code RE-DISPATCHES instead of coalescing onto the already-settled promise
    // forever. (A successful result would be served from the token cache without
    // re-dispatching, so null is what actually exercises the cleanup.)
    const p1 = tokenizeAsync('bash', 'echo uncacheable')
    expect(workers[0].messages).toHaveLength(2)
    workers[0].onmessage?.({ data: { id: workers[0].messages[1].id, tokens: null } } as MessageEvent)
    await expect(p1).resolves.toBeNull()

    const p2 = tokenizeAsync('bash', 'echo uncacheable')
    expect(workers[0].messages).toHaveLength(3) // re-dispatched, not coalesced onto the settled null
    workers[0].onmessage?.({ data: { id: workers[0].messages[2].id, tokens: null } } as MessageEvent)
    await expect(p2).resolves.toBeNull()
  })

  it('ignores a stale error event from a replaced worker', async () => {
    const tokens = [[{ content: 'second', htmlStyle: {} }]]
    const workers: Array<{
      onmessage: ((event: MessageEvent) => void) | null
      onerror: (() => void) | null
      messages: Array<{ id: number, lang: string, code: string }>
      terminate: ReturnType<typeof vi.fn>
    }> = []
    Object.defineProperty(globalThis, 'Worker', {
      configurable: true,
      writable: true,
      value: class CapturingWorker {
        onmessage: ((event: MessageEvent) => void) | null = null
        onerror: (() => void) | null = null
        messages: Array<{ id: number, lang: string, code: string }> = []
        terminate = vi.fn()

        constructor() {
          workers.push(this)
        }

        postMessage(message: { id: number, lang: string, code: string }) {
          this.messages.push(message)
        }
      },
    })
    const { tokenizeAsync } = await importClient()

    const first = tokenizeAsync('bash', 'echo first')
    workers[0].onerror?.()
    await expect(first).resolves.toBeNull()

    const second = tokenizeAsync('bash', 'echo second')
    let settled = false
    second.then(() => {
      settled = true
    })
    workers[0].onerror?.()
    await Promise.resolve()
    expect(settled).toBe(false)

    const { id } = workers[1].messages[0]
    workers[1].onmessage?.({ data: { id, tokens } } as MessageEvent)
    await expect(second).resolves.toEqual(tokens)
  })
})
