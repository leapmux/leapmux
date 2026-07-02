import { IDBFactory } from 'fake-indexeddb'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { shikiStyleClassName } from './shikiStyleClass'

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
    // The wire carries the interned shape; the client expands it, minting the
    // shared style class per distinct style (see tokenCache / shikiStyleClass).
    const wireTokens = { styles: [{ color: 'red' }], lines: [[[0, 'echo']]] }
    const tokens = [[{ content: 'echo', className: shikiStyleClassName('color:red') }]]
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
    workers[0].onmessage?.({ data: { id, tokens: wireTokens } } as MessageEvent)
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
    const wireTokens = { styles: [], lines: [[[-1, 'second']]] }
    const tokens = [[{ content: 'second' }]]
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
    workers[1].onmessage?.({ data: { id, tokens: wireTokens } } as MessageEvent)
    await expect(second).resolves.toEqual(tokens)
  })

  it('serves persisted tokens from the artifact store without spawning a worker', async () => {
    vi.stubGlobal('indexedDB', new IDBFactory())
    const workers: unknown[] = []
    Object.defineProperty(globalThis, 'Worker', {
      configurable: true,
      writable: true,
      value: class NeverNeededWorker {
        constructor() {
          workers.push(this)
        }

        postMessage() {}
        terminate() {}
      },
    })
    try {
      const { tokenizeAsync, TOKEN_ARTIFACT_NS } = await importClient()
      // Fresh module registry: this store instance is the one the client uses.
      const store = await import('./renderArtifactStore')
      const { makeKey } = await import('./tokenCache')
      await store.putArtifact(
        TOKEN_ARTIFACT_NS,
        makeKey('bash', 'echo persisted'),
        { styles: [{ color: 'red' }], lines: [[[0, 'echo persisted']]] },
      )

      await expect(tokenizeAsync('bash', 'echo persisted')).resolves.toEqual(
        [[{ content: 'echo persisted', className: shikiStyleClassName('color:red') }]],
      )
      expect(workers).toHaveLength(0) // the reload warm-start never touched a worker
    }
    finally {
      vi.unstubAllGlobals()
    }
  })

  it('persists a successful tokenization for the next session', async () => {
    vi.stubGlobal('indexedDB', new IDBFactory())
    const workers: Array<{
      onmessage: ((event: MessageEvent) => void) | null
      messages: Array<{ id: number, lang: string, code: string }>
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
    try {
      const { tokenizeAsync, TOKEN_ARTIFACT_NS } = await importClient()
      const store = await import('./renderArtifactStore')
      const { makeKey } = await import('./tokenCache')

      const pending = tokenizeAsync('bash', 'echo save-me')
      // The dispatch sits behind the store miss, so wait for the message.
      await vi.waitFor(() => expect(workers[0]?.messages ?? []).toHaveLength(1))
      const wire = { styles: [], lines: [[[-1, 'echo save-me']]] }
      workers[0].onmessage?.({ data: { id: workers[0].messages[0].id, tokens: wire } } as MessageEvent)
      await expect(pending).resolves.toEqual([[{ content: 'echo save-me' }]])

      // The result landed in the persistent store VERBATIM in the wire shape —
      // the interned form carries the declarations a later session needs to
      // re-mint the style classes (the expanded form only has class names).
      await vi.waitFor(async () => {
        await expect(store.getArtifact(TOKEN_ARTIFACT_NS, makeKey('bash', 'echo save-me')))
          .resolves
          .toEqual(wire)
      })
    }
    finally {
      vi.unstubAllGlobals()
    }
  })
})
