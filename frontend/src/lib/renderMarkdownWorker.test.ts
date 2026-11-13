import { IDBFactory } from 'fake-indexeddb'
import { createEffect, createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// Mock the worker bridge so the async render path is driven deterministically:
// each test states the reply of each render, and when it lands.
vi.mock('~/lib/markdownWorkerClient', () => ({
  renderMarkdownInWorker: vi.fn(),
}))

const { renderMarkdownInWorker } = await import('~/lib/markdownWorkerClient')
const {
  _getMarkdownCacheChars,
  _getMarkdownCacheSize,
  _resetMarkdownCache,
  getCachedMarkdownHtml,
  MARKDOWN_CACHE_MAX_CHARS,
  markdownArtifactNs,
  prefetchMarkdownHtml,
  requestMarkdownHtml,
} = await import('~/lib/renderMarkdown')
const { syntaxThemePair } = await import('~/lib/shikiThemes')
const { MARKDOWN_SOURCE_FALLBACK_ATTRIBUTE } = await import('~/lib/markdownSourceFallback')
const artifactStore = await import('~/lib/renderArtifactStore')
const shikiStyleClass = await import('~/lib/shikiStyleClass')
const { readInjectedShikiRules } = await import('~/lib/shikiStyleClass.testkit')
const { shikiHighlighter } = await import('~/lib/shikiSyncHighlighter')
const { syntaxPairFor } = await import('~/lib/syntaxThemes')
const { setSyntaxTheme } = await import('~/lib/syntaxThemeStore')
const { DEFAULT_THEME_ID } = await import('~/styles/themes')

type RenderResult = Awaited<ReturnType<typeof renderMarkdownInWorker>>

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

  it('caches the escaped source when the worker bridge throws synchronously', async () => {
    const text = 'sync throw worker'
    mockWorker.mockImplementation(() => {
      throw new Error('worker blocked by policy')
    })

    // A throw takes the path of a rejection: pending, then the source.
    expect(requestMarkdownHtml(text)).toBeUndefined()
    await flushMicrotasks()
    mockWorker.mockClear()

    const html = requestMarkdownHtml(text)
    expect(html).toContain(MARKDOWN_SOURCE_FALLBACK_ATTRIBUTE)
    expect(html).toContain(text)
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

describe('requestMarkdownHtml replies that it does not cache', () => {
  beforeEach(() => {
    _resetMarkdownCache()
    shikiStyleClass._resetShikiStyleClassesForTest()
    mockWorker.mockReset()
  })

  afterEach(async () => {
    vi.unstubAllGlobals()
    await setSyntaxTheme(syntaxPairFor(DEFAULT_THEME_ID), shikiHighlighter)
  })

  /** Hand each worker call a render that this test answers through the returned list. */
  function holdRenders(): Array<(result: RenderResult) => void> {
    const answers: Array<(result: RenderResult) => void> = []
    mockWorker.mockImplementation(() => new Promise((resolve) => {
      answers.push(resolve)
    }))
    return answers
  }

  function answer(answers: Array<(result: RenderResult) => void>, index: number, result: RenderResult): void {
    const resolve = answers[index]
    if (resolve === undefined)
      throw new Error(`expected worker call ${index}`)
    resolve(result)
  }

  /** Change the syntax theme away and back, which abandons every render in flight. */
  async function leaveThemeAndReturn(): Promise<void> {
    await setSyntaxTheme(syntaxPairFor('nord'), shikiHighlighter)
    await setSyntaxTheme(syntaxPairFor(DEFAULT_THEME_ID), shikiHighlighter)
  }

  it('keeps waiting for the newer render of a body when a render that a theme change abandoned lands', async () => {
    const text = 'abandoned by a theme change'
    const answers = holdRenders()
    requestMarkdownHtml(text)
    await leaveThemeAndReturn()
    requestMarkdownHtml(text)
    expect(mockWorker).toHaveBeenCalledTimes(2)

    // The abandoned render degraded. Its theme is current again, but it no longer
    // owns the body: the newer render does.
    answer(answers, 0, { html: '<p>degraded</p>', retryable: true, styles: {} })
    await flushMicrotasks()

    expect(requestMarkdownHtml(text)).toBeUndefined()
    expect(mockWorker).toHaveBeenCalledTimes(2)

    answer(answers, 1, { html: '<p>newer</p>', retryable: false, styles: {} })
    await flushMicrotasks()
    expect(requestMarkdownHtml(text)).toBe('<p>newer</p>')
  })

  it('injects the style rules of a retryable reply, which the worker sends only once', async () => {
    const answers = holdRenders()
    requestMarkdownHtml('retryable body with a new token style')

    answer(answers, 0, {
      html: '<pre>degraded</pre>',
      retryable: true,
      styles: { 'sk-000000c1-13': '--shiki-light:#c1c' },
    })
    await flushMicrotasks()

    expect(readInjectedShikiRules()).toContain('.sk-000000c1-13{--shiki-light:#c1c}')
  })

  it('injects the style rules of a reply that lands under another theme', async () => {
    const answers = holdRenders()
    requestMarkdownHtml('abandoned body with a new token style')
    await setSyntaxTheme(syntaxPairFor('nord'), shikiHighlighter)

    answer(answers, 0, {
      html: '<pre>abandoned</pre>',
      retryable: false,
      styles: { 'sk-000000c2-13': '--shiki-light:#c2c' },
    })
    await flushMicrotasks()

    // The HTML belongs to the theme the user left, so nothing caches it. The rule
    // stays valid under any theme, and a later render can use its class.
    expect(getCachedMarkdownHtml('abandoned body with a new token style')).toBeUndefined()
    expect(readInjectedShikiRules()).toContain('.sk-000000c2-13{--shiki-light:#c2c}')
  })
})

describe('requestMarkdownHtml cache size', () => {
  beforeEach(() => {
    _resetMarkdownCache()
    mockWorker.mockReset()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  /** Render `text` to `html` through the worker, and wait until the cache holds it. */
  async function renderToCache(text: string, html: string): Promise<void> {
    mockWorker.mockResolvedValueOnce({ html, retryable: false, styles: {} })
    requestMarkdownHtml(text)
    await flushMicrotasks()
  }

  it('evicts the least recently used bodies once the cache holds more characters than its budget', async () => {
    // A quarter of the budget each, plus the source: three bodies fit, a fourth does not.
    const html = 'h'.repeat(MARKDOWN_CACHE_MAX_CHARS / 4)
    const bodies = ['a', 'b', 'c', 'd', 'e'].map(name => name.repeat(1024))

    for (const body of bodies)
      await renderToCache(body, html)

    expect(_getMarkdownCacheChars()).toBeLessThanOrEqual(MARKDOWN_CACHE_MAX_CHARS)
    expect(bodies.map(body => getCachedMarkdownHtml(body) !== undefined)).toEqual([false, false, true, true, true])
  })

  // Small bodies stay far below the character budget, so only the entry cap evicts
  // them. Every write goes through the one helper that keeps both limits.
  it('keeps its entry cap beside the character budget, and evicts the oldest bodies past it', async () => {
    const bodies = Array.from({ length: 1_100 }, (_, index) => `body ${index}`)
    for (const body of bodies)
      await renderToCache(body, `<p>${body}</p>`)

    const kept = bodies.filter(body => getCachedMarkdownHtml(body) !== undefined)
    expect(kept.length).toBe(_getMarkdownCacheSize())
    expect(kept.length).toBeLessThan(bodies.length)
    expect(kept).toEqual(bodies.slice(bodies.length - kept.length))
  })

  // The eviction runs from the least recently USED end, not from the oldest write:
  // a body that a reader asked for since it landed is the one the reader needs.
  it('keeps a body that a reader asked for since it was cached, and evicts an older unread body in its place', async () => {
    const html = 'h'.repeat(MARKDOWN_CACHE_MAX_CHARS / 4)
    const bodies = ['a', 'b', 'c', 'd'].map(name => name.repeat(1024))
    const [first, second, third, fourth] = bodies as [string, string, string, string]
    for (const body of [first, second, third])
      await renderToCache(body, html)

    // The oldest body is shown again, so it becomes the most recently used.
    expect(requestMarkdownHtml(first)).toBe(html)
    await renderToCache(fourth, html)

    expect(_getMarkdownCacheChars()).toBeLessThanOrEqual(MARKDOWN_CACHE_MAX_CHARS)
    expect(bodies.map(body => getCachedMarkdownHtml(body) !== undefined)).toEqual([true, false, true, true])
    expect(mockWorker).toHaveBeenCalledTimes(4)
  })

  it('keeps a body that alone holds more characters than the budget, and nothing else', async () => {
    await renderToCache('small body', '<p>small body</p>')
    const huge = 'x'.repeat(MARKDOWN_CACHE_MAX_CHARS)

    await renderToCache('huge body', huge)

    // A consumer waits for the huge body and reads it after the version bump. A miss
    // there would dispatch it again, and again after each reply.
    expect(requestMarkdownHtml('huge body')).toBe(huge)
    expect(_getMarkdownCacheSize()).toBe(1)
    expect(mockWorker).toHaveBeenCalledTimes(2)
  })
})

describe('requestMarkdownHtml render priority', () => {
  beforeEach(() => {
    _resetMarkdownCache()
    mockWorker.mockReset()
    mockWorker.mockReturnValue(new Promise(() => {}))
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

  it('keeps a body that a visible row waits for at full priority after an offscreen row asks for it too', () => {
    requestMarkdownHtml('shared body', () => false)
    requestMarkdownHtml('shared body', () => true)
    expect(priorityOfCall()()).toBe(false)
  })

  it('lets a shared body wait once every row that asked for it is offscreen', () => {
    let firstOffscreen = false
    requestMarkdownHtml('shared body', () => firstOffscreen)
    requestMarkdownHtml('shared body', () => true)
    const isLow = priorityOfCall()

    expect(isLow()).toBe(false)
    firstOffscreen = true
    expect(isLow()).toBe(true)
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
})

describe('requestMarkdownHtml persisted render artifacts (indexeddb warm-start)', () => {
  beforeEach(() => {
    _resetMarkdownCache()
    mockWorker.mockReset()
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

  it('dispatches to the worker when the persisted read fails', async () => {
    const text = 'persisted body whose read fails'
    vi.spyOn(artifactStore, 'getArtifact').mockRejectedValueOnce(new Error('the store is closed'))
    mockWorker.mockResolvedValue({ html: '<p>WORKER</p>', retryable: false, styles: {} })

    expect(requestMarkdownHtml(text)).toBeUndefined()

    // A failed read must not leave the body in flight forever: its row would stay hidden.
    await vi.waitFor(() => {
      expect(requestMarkdownHtml(text)).toBe('<p>WORKER</p>')
    })
    expect(mockWorker).toHaveBeenCalledTimes(1)
  })

  it('caches the escaped source, and starts no render, when serving the persisted render fails', async () => {
    const text = 'persisted body that fails to serve'
    await artifactStore.putArtifact(markdownArtifactNs(), text, { h: '<p>PERSISTED</p>', s: {} })
    // The completion throws while it injects the persisted style rules.
    vi.spyOn(shikiStyleClass, 'ensureShikiStyleRules').mockImplementationOnce(() => {
      throw new Error('style injection failed')
    })
    mockWorker.mockReturnValue(new Promise(() => {}))

    expect(requestMarkdownHtml(text)).toBeUndefined()

    // The body ends like one whose worker reply could not be served: as its source.
    // A worker render started here would run beside the next request's own.
    await vi.waitFor(() => {
      expect(requestMarkdownHtml(text)).toContain(MARKDOWN_SOURCE_FALLBACK_ATTRIBUTE)
    })
    expect(mockWorker).not.toHaveBeenCalled()
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

  it('persists a clean worker render with the declaration of each class that its HTML uses', async () => {
    const text = 'persist this body'
    const html = '<pre class="shiki"><code><span class="sk-0000000d-13">CLEAN</span></code></pre>'
    mockWorker.mockResolvedValue({
      html,
      retryable: false,
      // A reply can ship a class that its HTML does not use: a render that falls back
      // to plain Markdown after Shiki minted the class does.
      styles: { 'sk-0000000d-13': '--shiki-light:#111', 'sk-0000000e-13': '--shiki-light:#222' },
    })
    requestMarkdownHtml(text)

    await vi.waitFor(async () => {
      await expect(artifactStore.getArtifact(markdownArtifactNs(), text))
        .resolves
        .toEqual({
          h: html,
          s: { 'sk-0000000d-13': '--shiki-light:#111' },
        })
    })
  })

  // The store limits the HTML of one entry, so that one pathological body cannot
  // take it over. The body still renders and caches in memory.
  it('persists no clean render whose HTML is longer than the store keeps for one body', async () => {
    const put = vi.spyOn(artifactStore, 'putArtifact')
    const longText = 'a body whose render is too long to persist'
    const longHtml = `<p>${'x'.repeat(512 * 1024)}</p>`
    mockWorker.mockResolvedValueOnce({ html: longHtml, retryable: false, styles: {} })
    requestMarkdownHtml(longText)
    // The store answers the read of each body first, and then the worker renders it.
    await vi.waitFor(() => {
      expect(getCachedMarkdownHtml(longText)).toBe(longHtml)
    })

    // A body whose HTML fits is persisted, so the store took the writes it was given.
    mockWorker.mockResolvedValueOnce({ html: '<p>short</p>', retryable: false, styles: {} })
    requestMarkdownHtml('a short body')
    await vi.waitFor(() => {
      expect(put).toHaveBeenCalledOnce()
    })

    expect(put.mock.calls[0]?.[1]).toBe('a short body')
    await expect(artifactStore.getArtifact(markdownArtifactNs(), longText)).resolves.toBeUndefined()
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
