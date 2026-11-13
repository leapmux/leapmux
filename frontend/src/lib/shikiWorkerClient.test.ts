import { IDBFactory } from 'fake-indexeddb'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { CapturingWorker, installCapturingWorkers } from '~/test-support/fakeWorker'
import { shikiStyleClassName } from './shikiStyleClass'

async function importClient() {
  vi.resetModules()
  return await import('./shikiWorkerClient')
}

// Captured-worker/message lookup: each test pushes exactly the entries it
// expects, so a missing entry is a harness failure, not a case to guard.
function captured<T>(list: readonly T[], index: number): T {
  const value = list[index]
  if (value === undefined)
    throw new Error(`expected a captured entry at index ${index}`)
  return value
}

/** The message that the client posts to the token worker. */
interface PostedTokenize {
  id: number
  lang: string
  code: string
  syntax: { light: string, dark: string }
}

/** A worker stand-in that records each message, and that the test answers by hand. */
type TokenWorker = CapturingWorker<PostedTokenize>

/** Answer the message at `index` with plain tokens of its own code. */
function answerPlain(worker: TokenWorker, index: number): void {
  const message = captured(worker.messages, index)
  worker.reply({
    id: message.id,
    tokens: {
      styles: [],
      lines: [
        [
          [-1, message.code],
        ],
      ],
    },
  })
}

describe('shikiWorkerClient', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('returns null where no Worker exists, as for a worker that cannot start', async () => {
    vi.stubGlobal('Worker', undefined)

    const { tokenizeAsync } = await importClient()

    await expect(tokenizeAsync('bash', 'echo no-worker')).resolves.toBeNull()
  })

  it('returns null when creating the worker throws synchronously', async () => {
    vi.stubGlobal('Worker', class ThrowingWorker {
      constructor() {
        throw new Error('blocked by CSP')
      }
    })

    const { tokenizeAsync } = await importClient()

    await expect(tokenizeAsync('bash', 'echo no-worker')).resolves.toBeNull()
  })

  // The worker's lifecycle after a failed post (the worker stays, and only that
  // request fails) belongs to `createWorkerClient`, and its own tests pin it.
  // These two pin what the token client adds: a null result, and the slot of
  // the gate that the failed request frees.
  it('returns null when postMessage throws synchronously', async () => {
    vi.stubGlobal('Worker', class PostThrowingWorker {
      onmessage: ((event: MessageEvent) => void) | null = null
      onerror: (() => void) | null = null
      terminate = vi.fn()

      postMessage() {
        throw new Error('the message cannot be cloned')
      }
    })

    const { tokenizeAsync } = await importClient()

    await expect(tokenizeAsync('bash', 'echo post-fail')).resolves.toBeNull()
  })

  it('frees the slot of a request whose postMessage throws, and still answers the older one', async () => {
    const workers: TokenWorker[] = []
    vi.stubGlobal('Worker', class FlakyWorker extends CapturingWorker<PostedTokenize> {
      constructor() {
        super()
        workers.push(this)
      }

      override postMessage(message: PostedTokenize) {
        if (message.code === 'echo second')
          throw new Error('the message cannot be cloned')
        super.postMessage(message)
      }
    })

    const { tokenizeAsync } = await importClient()

    const first = tokenizeAsync('bash', 'echo first')
    const second = tokenizeAsync('bash', 'echo second')
    await expect(second).resolves.toBeNull()

    // The gate holds two requests at most, and the failed one left its slot.
    const third = tokenizeAsync('bash', 'echo third')
    const worker = captured(workers, 0)
    expect(worker.messages.map(message => message.code)).toEqual(['echo first', 'echo third'])

    answerPlain(worker, 0)
    answerPlain(worker, 1)
    await expect(first).resolves.toEqual([
      [
        { content: 'echo first' },
      ],
    ])
    await expect(third).resolves.toEqual([
      [
        { content: 'echo third' },
      ],
    ])
  })

  it('coalesces concurrent identical requests onto one worker dispatch, then re-dispatches after it settles', async () => {
    // The wire carries the interned shape; the client expands it, minting the
    // shared style class per distinct style (see tokenCache / shikiStyleClass).
    const wireTokens = {
      styles: [
        { color: 'red' },
      ],
      lines: [
        [
          [0, 'echo'],
        ],
      ],
    }
    const tokens = [
      [
        {
          content: 'echo',
          className: shikiStyleClassName('color:red'),
        },
      ],
    ]
    const workers = installCapturingWorkers<PostedTokenize>()
    const { tokenizeAsync } = await importClient()

    // Two concurrent requests for the SAME (lang, code) must dispatch ONCE.
    const a = tokenizeAsync('bash', 'echo dedup')
    const b = tokenizeAsync('bash', 'echo dedup')
    expect(captured(workers, 0).messages).toHaveLength(1)

    // Resolving the single dispatch resolves BOTH callers with the same tokens.
    const { id } = captured(captured(workers, 0).messages, 0)
    captured(workers, 0).reply({ id, tokens: wireTokens })
    await expect(a).resolves.toEqual(tokens)
    await expect(b).resolves.toEqual(tokens)

    // DIFFERENT code is never coalesced, and -- crucially -- a NULL result is NOT cached,
    // so once its in-flight entry is dropped (the `.finally` cleanup) a later request for
    // that code RE-DISPATCHES instead of coalescing onto the already-settled promise
    // forever. (A successful result would be served from the token cache without
    // re-dispatching, so null is what actually exercises the cleanup.)
    const p1 = tokenizeAsync('bash', 'echo uncacheable')
    expect(captured(workers, 0).messages).toHaveLength(2)
    captured(workers, 0).reply({ id: captured(captured(workers, 0).messages, 1).id, tokens: null })
    await expect(p1).resolves.toBeNull()

    const p2 = tokenizeAsync('bash', 'echo uncacheable')
    expect(captured(workers, 0).messages).toHaveLength(3) // re-dispatched, not coalesced onto the settled null
    captured(workers, 0).reply({ id: captured(captured(workers, 0).messages, 2).id, tokens: null })
    await expect(p2).resolves.toBeNull()
  })

  it('ignores a stale error event from a replaced worker', async () => {
    const wireTokens = {
      styles: [],
      lines: [
        [
          [-1, 'second'],
        ],
      ],
    }
    const tokens = [
      [
        { content: 'second' },
      ],
    ]
    const workers = installCapturingWorkers<PostedTokenize>()
    const { tokenizeAsync } = await importClient()

    const first = tokenizeAsync('bash', 'echo first')
    captured(workers, 0).onerror?.()
    await expect(first).resolves.toBeNull()

    const second = tokenizeAsync('bash', 'echo second')
    let settled = false
    second.then(() => {
      settled = true
    })
    captured(workers, 0).onerror?.()
    await Promise.resolve()
    expect(settled).toBe(false)

    const { id } = captured(captured(workers, 1).messages, 0)
    captured(workers, 1).reply({ id, tokens: wireTokens })
    await expect(second).resolves.toEqual(tokens)
  })

  it('serves persisted tokens from the full tool output store without spawning a worker', async () => {
    vi.stubGlobal('indexedDB', new IDBFactory())
    const workers = installCapturingWorkers<PostedTokenize>()
    try {
      const { tokenizeAsync, tokenArtifactNs } = await importClient()
      // Fresh module registry: this store instance is the one the client uses.
      const store = await import('./renderArtifactStore')
      const { makeKey } = await import('./tokenCache')
      await store.putArtifact(
        tokenArtifactNs(),
        makeKey('bash', 'echo persisted'),
        {
          styles: [
            { color: 'red' },
          ],
          lines: [
            [
              [0, 'echo persisted'],
            ],
          ],
        },
      )

      await expect(tokenizeAsync('bash', 'echo persisted')).resolves.toEqual(
        [
          [
            {
              content: 'echo persisted',
              className: shikiStyleClassName('color:red'),
            },
          ],
        ],
      )
      expect(workers).toHaveLength(0) // the reload warm-start never touched a worker
    }
    finally {
      vi.unstubAllGlobals()
    }
  })

  it('ignores malformed persisted tokens and falls back to the worker', async () => {
    vi.stubGlobal('indexedDB', new IDBFactory())
    const workers = installCapturingWorkers<PostedTokenize>()
    try {
      const { tokenizeAsync, tokenArtifactNs } = await importClient()
      const store = await import('./renderArtifactStore')
      const { makeKey } = await import('./tokenCache')
      await store.putArtifact(
        tokenArtifactNs(),
        makeKey('bash', 'echo fallback'),
        {
          styles: [],
          lines: [
            [null],
          ],
        },
      )

      const pending = tokenizeAsync('bash', 'echo fallback')
      await vi.waitFor(() => expect(workers[0]?.messages ?? []).toHaveLength(1))
      const wire = {
        styles: [],
        lines: [
          [
            [-1, 'echo fallback'],
          ],
        ],
      }
      const worker = captured(workers, 0)
      worker.reply({ id: captured(worker.messages, 0).id, tokens: wire })
      await expect(pending).resolves.toEqual([
        [
          { content: 'echo fallback' },
        ],
      ])
    }
    finally {
      vi.unstubAllGlobals()
    }
  })

  it('persists a successful tokenization for the next session', async () => {
    vi.stubGlobal('indexedDB', new IDBFactory())
    const workers = installCapturingWorkers<PostedTokenize>()
    try {
      const { tokenizeAsync, tokenArtifactNs } = await importClient()
      const store = await import('./renderArtifactStore')
      const { makeKey } = await import('./tokenCache')

      const pending = tokenizeAsync('bash', 'echo save-me')
      // The dispatch sits behind the store miss, so wait for the message.
      await vi.waitFor(() => expect(workers[0]?.messages ?? []).toHaveLength(1))
      const wire = {
        styles: [],
        lines: [
          [
            [-1, 'echo save-me'],
          ],
        ],
      }
      const worker = captured(workers, 0)
      worker.reply({ id: captured(worker.messages, 0).id, tokens: wire })
      await expect(pending).resolves.toEqual([
        [
          { content: 'echo save-me' },
        ],
      ])

      // The result landed in the persistent store VERBATIM in the wire shape —
      // the interned form carries the declarations a later session needs to
      // re-mint the style classes (the expanded form only has class names).
      await vi.waitFor(async () => {
        await expect(store.getArtifact(tokenArtifactNs(), makeKey('bash', 'echo save-me')))
          .resolves
          .toEqual(wire)
      })
    }
    finally {
      vi.unstubAllGlobals()
    }
  })

  // The code of an offscreen row waits behind the code of the rows that a reader sees.
  it('posts a low-priority request after a later request that a reader needs now', async () => {
    const workers = installCapturingWorkers<PostedTokenize>()
    const { tokenizeAsync } = await importClient()
    // Two requests fill both slots of the gate.
    const busy = [
      tokenizeAsync('bash', 'echo busy-1'),
      tokenizeAsync('bash', 'echo busy-2'),
    ]
    const offscreen = tokenizeAsync('bash', 'echo offscreen', () => true)
    const visible = tokenizeAsync('bash', 'echo visible')
    const worker = captured(workers, 0)

    answerPlain(worker, 0)
    await busy[0]
    expect(captured(worker.messages, 2).code).toBe('echo visible')

    answerPlain(worker, 1)
    await busy[1]
    expect(captured(worker.messages, 3).code).toBe('echo offscreen')
    answerPlain(worker, 2)
    answerPlain(worker, 3)
    await Promise.all([visible, offscreen])
  })

  describe('tokenizeUncached', () => {
    const pair = { light: 'nord', dark: 'nord' }

    it('returns null where no Worker exists, as for a worker that cannot start', async () => {
      vi.stubGlobal('Worker', undefined)

      const { tokenizeUncached } = await importClient()

      await expect(tokenizeUncached('bash', 'echo no-worker', pair)).resolves.toBeNull()
    })

    it('returns null when the worker crashes', async () => {
      const workers = installCapturingWorkers<PostedTokenize>()
      const { tokenizeUncached } = await importClient()

      const pending = tokenizeUncached('bash', 'echo crash', pair)
      captured(workers, 0).onerror?.()

      await expect(pending).resolves.toBeNull()
    })

    it('tokenizes under the pair that it receives, and leaves the token cache alone', async () => {
      const workers = installCapturingWorkers<PostedTokenize>()
      const { tokenizeAsync, tokenizeUncached } = await importClient()
      // The fresh module registry: the cache that the client writes.
      const { getCachedTokens } = await import('./tokenCache')

      const pending = tokenizeUncached('bash', 'echo uncached', pair)
      const worker = captured(workers, 0)
      expect(captured(worker.messages, 0)).toMatchObject({
        type: 'tokenize',
        lang: 'bash',
        code: 'echo uncached',
        syntax: pair,
      })
      answerPlain(worker, 0)

      await expect(pending).resolves.toEqual([
        [
          { content: 'echo uncached' },
        ],
      ])
      expect(getCachedTokens('bash', 'echo uncached')).toBeUndefined()
      // The cached path finds nothing either, so it asks the worker itself.
      void tokenizeAsync('bash', 'echo uncached')
      expect(worker.messages).toHaveLength(2)
    })

    it('sends each call to the worker, even an identical one', async () => {
      const workers = installCapturingWorkers<PostedTokenize>()
      const { tokenizeUncached } = await importClient()

      const first = tokenizeUncached('bash', 'echo twice', pair)
      const second = tokenizeUncached('bash', 'echo twice', pair)

      const worker = captured(workers, 0)
      expect(worker.messages).toHaveLength(2)
      answerPlain(worker, 0)
      answerPlain(worker, 1)
      await expect(first).resolves.toEqual([
        [
          { content: 'echo twice' },
        ],
      ])
      await expect(second).resolves.toEqual([
        [
          { content: 'echo twice' },
        ],
      ])
    })

    it('writes nothing to the persistent store', async () => {
      vi.stubGlobal('indexedDB', new IDBFactory())
      try {
        const workers = installCapturingWorkers<PostedTokenize>()
        const { tokenizeAsync, tokenizeUncached, tokenArtifactNs } = await importClient()
        const store = await import('./renderArtifactStore')
        const { makeKey } = await import('./tokenCache')

        const uncached = tokenizeUncached('bash', 'echo skip-store', { light: 'github-light', dark: 'github-dark' })
        const worker = captured(workers, 0)
        answerPlain(worker, 0)
        await uncached

        // A cached request after it, whose write the test can wait for. The
        // store applies the writes in order, so a write of the uncached text
        // would land first.
        const persisted = tokenizeAsync('bash', 'echo save-me')
        await vi.waitFor(() => expect(worker.messages).toHaveLength(2))
        answerPlain(worker, 1)
        await persisted
        await vi.waitFor(async () => {
          await expect(store.getArtifact(tokenArtifactNs(), makeKey('bash', 'echo save-me'))).resolves.toBeDefined()
        })

        await expect(store.getArtifact(tokenArtifactNs(), makeKey('bash', 'echo skip-store'))).resolves.toBeUndefined()
      }
      finally {
        vi.unstubAllGlobals()
      }
    })

    it('goes ahead of the queued requests of the chat', async () => {
      const workers = installCapturingWorkers<PostedTokenize>()
      const { tokenizeAsync, tokenizeUncached } = await importClient()

      // Two requests fill both slots of the gate, and a third waits.
      const busy = [
        tokenizeAsync('bash', 'echo busy-1'),
        tokenizeAsync('bash', 'echo busy-2'),
      ]
      const queued = tokenizeAsync('bash', 'echo queued')
      const typed = tokenizeUncached('bash', 'echo typed', pair)
      const worker = captured(workers, 0)
      expect(worker.messages.map(message => message.code)).toEqual(['echo busy-1', 'echo busy-2'])

      answerPlain(worker, 0)
      await busy[0]

      // The composer's request took the free slot.
      expect(captured(worker.messages, 2).code).toBe('echo typed')

      answerPlain(worker, 1)
      answerPlain(worker, 2)
      await Promise.all([busy[1], typed])
      expect(captured(worker.messages, 3).code).toBe('echo queued')
      answerPlain(worker, 3)
      await queued
    })
  })
})
