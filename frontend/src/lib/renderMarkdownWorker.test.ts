import { IDBFactory } from 'fake-indexeddb'
import { createEffect, createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// Mock the worker bridge so the async render path is driven deterministically
// (jsdom defines no real Worker, and we never want to spawn one in a unit test).
vi.mock('~/lib/markdownWorkerClient', () => ({
  renderMarkdownInWorker: vi.fn(),
}))

const { renderMarkdownInWorker } = await import('~/lib/markdownWorkerClient')
const { _getMarkdownCacheSize, _resetMarkdownCache, markdownArtifactNs, prefetchMarkdownHtml, requestMarkdownHtml } = await import('~/lib/renderMarkdown')
const { syntaxThemePair } = await import('~/lib/shikiThemes')
const { MARKDOWN_SOURCE_FALLBACK_ATTRIBUTE } = await import('~/lib/markdownSourceFallback')
const artifactStore = await import('~/lib/renderArtifactStore')
const shikiStyleClass = await import('~/lib/shikiStyleClass')

const mockWorker = vi.mocked(renderMarkdownInWorker)

/** Flush the worker `.then` microtask plus the coalesced version-bump microtask. */
async function flushMicrotasks() {
  await Promise.resolve()
  await Promise.resolve()
  await Promise.resolve()
}

/** The priority thunk that the render of the `index`th worker call hands the gate. */
function priorityOfCall(index = 0): () => boolean {
  const thunk = mockWorker.mock.calls[index]?.[2]
  if (thunk === undefined)
    throw new Error(`expected worker call ${index} to carry a priority thunk`)
  return thunk
}

describe('requestMarkdownHtml off-thread render path', () => {
  beforeEach(() => {
    _resetMarkdownCache()
    mockWorker.mockReset()
    // Make canUseWorker() true: requestMarkdownHtml reads `typeof Worker`.
    vi.stubGlobal('Worker', class {})
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('returns undefined while the worker renders, and never parses on the main thread', () => {
    mockWorker.mockReturnValue(new Promise(() => {})) // never resolves: stays pending
    const text = '```js\nconst x = 1\n```'

    expect(requestMarkdownHtml(text)).toBeUndefined()
    // Dispatch captures the theme pair and its render-artifact namespace together.
    // A later theme read could render one theme and store it under another namespace.
    expect(mockWorker).toHaveBeenCalledWith(text, syntaxThemePair(), expect.any(Function))
    expect(_getMarkdownCacheSize()).toBe(0)
  })

  it('stays pending for a body far past the size that used to switch to plain text', () => {
    mockWorker.mockReturnValue(new Promise(() => {}))
    const text = `# Large\n\n${'paragraph text '.repeat(70_000)}`

    expect(requestMarkdownHtml(text)).toBeUndefined()
    expect(mockWorker).toHaveBeenCalledTimes(1)
  })

  it('answers an empty body at once without dispatching', () => {
    expect(requestMarkdownHtml('')).toBe('')
    expect(mockWorker).not.toHaveBeenCalled()
  })

  it('caches the worker result so a later (reactive) re-render returns the HTML', async () => {
    const text = '```js\nconst y = 2\n```'
    mockWorker.mockResolvedValue({ html: '<pre class="shiki">HIGHLIGHTED</pre>', retryable: false, styles: {} })
    expect(requestMarkdownHtml(text)).toBeUndefined()
    await flushMicrotasks()
    // The completion filled the cache; a re-render now serves the final HTML.
    expect(requestMarkdownHtml(text)).toBe('<pre class="shiki">HIGHLIGHTED</pre>')
    expect(mockWorker).toHaveBeenCalledTimes(1)
  })

  it('re-runs a reactive consumer when the render lands', async () => {
    const text = 'reactive body'
    let resolveRender: (value: { html: string, retryable: boolean, styles: Record<string, string> }) => void = () => {}
    mockWorker.mockReturnValue(new Promise((resolve) => {
      resolveRender = resolve
    }))
    const seen: Array<string | undefined> = []
    const dispose = createRoot((disposeRoot) => {
      createEffect(() => {
        seen.push(requestMarkdownHtml(text))
      })
      return disposeRoot
    })
    await flushMicrotasks()
    expect(seen).toEqual([undefined])

    resolveRender({ html: '<p>reactive body</p>', retryable: false, styles: {} })
    await flushMicrotasks()

    expect(seen).toEqual([undefined, '<p>reactive body</p>'])
    dispose()
  })

  it('dispatches only once per distinct text while a render is in flight', () => {
    mockWorker.mockReturnValue(new Promise(() => {}))
    requestMarkdownHtml('in flight once')
    requestMarkdownHtml('in flight once')
    prefetchMarkdownHtml('in flight once')
    requestMarkdownHtml('in flight once')
    expect(mockWorker).toHaveBeenCalledTimes(1)
  })

  it('caches the escaped source when the worker crashes (resolves null) -- no main-thread parse, no re-dispatch', async () => {
    const text = '# crash recovery <b>raw</b>'
    mockWorker.mockResolvedValue(null)
    requestMarkdownHtml(text)
    await flushMicrotasks()
    mockWorker.mockClear()

    const html = requestMarkdownHtml(text)
    // The source, escaped, never parsed: no heading element, no bold element.
    expect(html).toContain(MARKDOWN_SOURCE_FALLBACK_ATTRIBUTE)
    expect(html).toContain('# crash recovery')
    expect(html).not.toContain('<h1>')
    expect(html).not.toContain('<b>')
    expect(mockWorker).not.toHaveBeenCalled()
  })

  it('caches the escaped source when the worker rejects -- no stuck pending state or retry loop', async () => {
    const text = 'rejected worker'
    mockWorker.mockRejectedValue(new Error('worker crashed'))
    requestMarkdownHtml(text)
    await flushMicrotasks()
    mockWorker.mockClear()

    expect(requestMarkdownHtml(text)).toContain(MARKDOWN_SOURCE_FALLBACK_ATTRIBUTE)
    expect(mockWorker).not.toHaveBeenCalled()
  })

  it('answers with the escaped source at once when the worker bridge throws synchronously', () => {
    const text = 'sync throw worker'
    mockWorker.mockImplementation(() => {
      throw new Error('worker blocked by policy')
    })

    // The bridge failed in the same call, so the body is never pending.
    const html = requestMarkdownHtml(text)
    expect(html).toContain(MARKDOWN_SOURCE_FALLBACK_ATTRIBUTE)
    expect(html).toContain(text)
    mockWorker.mockClear()
    expect(requestMarkdownHtml(text)).toBe(html)
    expect(mockWorker).not.toHaveBeenCalled()
  })

  it('_resetMarkdownCache clears inFlight so the same text re-dispatches after a reset', () => {
    mockWorker.mockReturnValue(new Promise(() => {})) // never resolves -> stays in flight
    requestMarkdownHtml('stuck in flight')
    expect(mockWorker).toHaveBeenCalledTimes(1)
    // Without clearing inFlight, the dedup guard would skip the dispatch forever, so a
    // clear-and-retry could never actually retry.
    _resetMarkdownCache()
    requestMarkdownHtml('stuck in flight')
    expect(mockWorker).toHaveBeenCalledTimes(2)
  })

  it('does NOT cache a transiently-failed (retryable) render, so a later render re-dispatches', async () => {
    // Unlike a worker CRASH (null -> cached source, no retry), a retryable degrade (a
    // grammar chunk load failed transiently) must not be cached: a later render should
    // re-dispatch and pick up the recovered grammar.
    const text = '```rust\nfn main() {}\n```'
    mockWorker.mockResolvedValue({ html: '<pre>degraded</pre>', retryable: true, styles: {} })
    requestMarkdownHtml(text)
    expect(mockWorker).toHaveBeenCalledTimes(1)
    await flushMicrotasks()

    mockWorker.mockClear()
    expect(requestMarkdownHtml(text)).toBeUndefined()
    // Cache miss (the retryable render was not stored) -> re-dispatch, not a cache hit.
    expect(mockWorker).toHaveBeenCalledTimes(1)
  })

  it('applies the recovered render after a retryable render, in a reactive consumer', async () => {
    const text = '```rust\nfn recover() {}\n```'
    // First attempt degrades (retryable); the retry succeeds.
    mockWorker
      .mockResolvedValueOnce({ html: '<pre>degraded</pre>', retryable: true, styles: {} })
      .mockResolvedValueOnce({ html: '<pre class="shiki">recovered</pre>', retryable: false, styles: {} })

    let html: string | undefined
    let dispose: (() => void) | undefined
    createRoot((d) => {
      dispose = d
      createEffect(() => {
        html = requestMarkdownHtml(text)
      })
    })
    expect(mockWorker).toHaveBeenCalledTimes(1) // initial dispatch
    expect(html).toBeUndefined()

    // The retryable completion re-dispatches (via the version bump re-running the effect);
    // the successful retry then caches and paints the final HTML.
    for (let i = 0; i < 6; i++)
      await flushMicrotasks()

    expect(mockWorker).toHaveBeenCalledTimes(2)
    expect(html).toBe('<pre class="shiki">recovered</pre>')
    dispose?.()
  })

  it('limits retry dispatches and caches the degraded render after the last attempt', async () => {
    const text = '```rust\nfn stuck() {}\n```'
    // Every attempt keeps failing transiently.
    mockWorker.mockResolvedValue({ html: '<pre>always degraded</pre>', retryable: true, styles: {} })

    let dispose: (() => void) | undefined
    createRoot((d) => {
      dispose = d
      createEffect(() => {
        requestMarkdownHtml(text)
      })
    })
    // Complete every attempt in the capped retry loop.
    for (let i = 0; i < 12; i++)
      await flushMicrotasks()

    // 1 initial dispatch + MAX_MARKDOWN_RENDER_RETRIES (3) retries, then it stops.
    expect(mockWorker).toHaveBeenCalledTimes(4)

    // The degraded render is now cached, so further renders are cache hits (loop ended).
    mockWorker.mockClear()
    expect(requestMarkdownHtml(text)).toBe('<pre>always degraded</pre>')
    expect(mockWorker).not.toHaveBeenCalled()
    dispose?.()
  })
})

describe('requestMarkdownHtml render priority', () => {
  beforeEach(() => {
    _resetMarkdownCache()
    mockWorker.mockReset()
    mockWorker.mockReturnValue(new Promise(() => {}))
    vi.stubGlobal('Worker', class {})
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('renders a body at full priority for a consumer that passes no priority thunk', () => {
    requestMarkdownHtml('urgent body')
    expect(priorityOfCall()()).toBe(false)
  })

  it('follows the consumer row priority thunk at every dispatch opportunity', () => {
    let offscreen = true
    requestMarkdownHtml('row body', () => offscreen)
    const isLow = priorityOfCall()

    expect(isLow()).toBe(true)
    offscreen = false
    expect(isLow()).toBe(false)
  })

  it('keeps a prefetch low until a consumer asks for the same body', () => {
    prefetchMarkdownHtml('collapsed body')
    const isLow = priorityOfCall()
    expect(isLow()).toBe(true)

    // The reader expands the section: the body now has a consumer waiting.
    expect(requestMarkdownHtml('collapsed body')).toBeUndefined()

    expect(isLow()).toBe(false)
    expect(mockWorker).toHaveBeenCalledTimes(1)
  })

  it('stays urgent once any consumer asked without a thunk, whatever a later row thunk says', () => {
    requestMarkdownHtml('shared body')
    requestMarkdownHtml('shared body', () => true)
    expect(priorityOfCall()()).toBe(false)
  })

  it('does not prefetch an empty body or a cached one', async () => {
    mockWorker.mockResolvedValue({ html: '<p>cached</p>', retryable: false, styles: {} })
    requestMarkdownHtml('cached')
    await flushMicrotasks()
    mockWorker.mockClear()

    prefetchMarkdownHtml('')
    prefetchMarkdownHtml('cached')

    expect(mockWorker).not.toHaveBeenCalled()
  })

  it('does nothing without a Worker: the consumer renders synchronously when it mounts', () => {
    vi.unstubAllGlobals()
    prefetchMarkdownHtml('no worker body')
    expect(mockWorker).not.toHaveBeenCalled()
    expect(_getMarkdownCacheSize()).toBe(0)
  })
})

describe('requestMarkdownHtml persisted render artifacts (indexeddb warm-start)', () => {
  beforeEach(() => {
    _resetMarkdownCache()
    mockWorker.mockReset()
    vi.stubGlobal('Worker', class {})
    vi.stubGlobal('indexedDB', new IDBFactory())
    artifactStore._resetArtifactStoreForTest()
  })

  afterEach(() => {
    artifactStore._resetArtifactStoreForTest()
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('serves a persisted render without dispatching the worker', async () => {
    const text = '```js\nconst persisted = 1\n```'
    // The persisted shape is {h: html, s: styles}: the shared token-style class
    // dictionary must travel with the HTML (see shikiStyleClass).
    await artifactStore.putArtifact(markdownArtifactNs(), text, { h: '<pre class="shiki">PERSISTED</pre>', s: {} })

    // Pending while the store resolves.
    expect(requestMarkdownHtml(text)).toBeUndefined()

    await vi.waitFor(() => {
      expect(requestMarkdownHtml(text)).toBe('<pre class="shiki">PERSISTED</pre>')
    })
    expect(mockWorker).not.toHaveBeenCalled()
  })

  it('dispatches to the worker when serving the persisted render fails', async () => {
    const text = 'persisted body that fails to serve'
    await artifactStore.putArtifact(markdownArtifactNs(), text, { h: '<p>PERSISTED</p>', s: {} })
    // The first completion throws while it injects the persisted style rules.
    vi.spyOn(shikiStyleClass, 'ensureShikiStyleRules').mockImplementationOnce(() => {
      throw new Error('style injection failed')
    })
    mockWorker.mockResolvedValue({ html: '<p>WORKER</p>', retryable: false, styles: {} })

    expect(requestMarkdownHtml(text)).toBeUndefined()

    // A failed read must not leave the body in flight forever: its row would stay hidden.
    await vi.waitFor(() => {
      expect(requestMarkdownHtml(text)).toBe('<p>WORKER</p>')
    })
    expect(mockWorker).toHaveBeenCalledTimes(1)
  })

  it('ignores malformed persisted renders and falls back to the worker', async () => {
    const text = '```js\nconst fallback = 1\n```'
    await artifactStore.putArtifact(markdownArtifactNs(), text, {
      h: '<pre class="shiki">CORRUPT</pre>',
      s: { 'not a generated class': 42 },
    })
    mockWorker.mockResolvedValue({
      html: '<pre class="shiki">WORKER</pre>',
      retryable: false,
      styles: {},
    })

    expect(requestMarkdownHtml(text)).toBeUndefined()

    await vi.waitFor(() => {
      expect(requestMarkdownHtml(text)).toBe('<pre class="shiki">WORKER</pre>')
    })
    expect(mockWorker).toHaveBeenCalledTimes(1)
  })

  it('persists a clean worker render (HTML + style dictionary) for the next session', async () => {
    const text = 'persist this body'
    mockWorker.mockResolvedValue({
      html: '<pre class="shiki">CLEAN</pre>',
      retryable: false,
      styles: { 'sk-a-3': '--shiki-light:#111' },
    })
    requestMarkdownHtml(text)

    await vi.waitFor(async () => {
      await expect(artifactStore.getArtifact(markdownArtifactNs(), text))
        .resolves
        .toEqual({
          h: '<pre class="shiki">CLEAN</pre>',
          s: { 'sk-a-3': '--shiki-light:#111' },
        })
    })
  })

  it('never persists a retryable (transiently degraded) render', async () => {
    const text = '```rust\nfn degraded() {}\n```'
    mockWorker.mockResolvedValue({ html: '<pre>degraded</pre>', retryable: true, styles: {} })

    let dispose: (() => void) | undefined
    createRoot((d) => {
      dispose = d
      createEffect(() => {
        requestMarkdownHtml(text)
      })
    })
    // Complete the capped retry loop. Every attempt remains retryable.
    await vi.waitFor(() => {
      expect(mockWorker.mock.calls.length).toBeGreaterThanOrEqual(4)
    })
    // Exhaustion caches the degraded HTML in MEMORY only — nothing durable.
    await expect(artifactStore.getArtifact(markdownArtifactNs(), text)).resolves.toBeUndefined()
    dispose?.()
  })
})
