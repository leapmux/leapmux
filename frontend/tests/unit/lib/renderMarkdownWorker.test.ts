import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// Mock the worker bridge so the async highlight path is driven deterministically
// (jsdom defines no real Worker, and we never want to spawn one in a unit test).
vi.mock('~/lib/markdownWorkerClient', () => ({
  renderMarkdownInWorker: vi.fn(),
}))

const { renderMarkdownInWorker } = await import('~/lib/markdownWorkerClient')
const { _getPlaceholderCacheSize, _resetMarkdownCache, renderMarkdown } = await import('~/lib/renderMarkdown')

const mockWorker = renderMarkdownInWorker as unknown as ReturnType<typeof vi.fn>

/** Flush the worker `.then` microtask plus the coalesced version-bump microtask. */
async function flushMicrotasks() {
  await Promise.resolve()
  await Promise.resolve()
  await Promise.resolve()
}

describe('renderMarkdown off-thread highlight path', () => {
  beforeEach(() => {
    _resetMarkdownCache()
    mockWorker.mockReset()
    // Make canUseWorker() true: renderMarkdown reads `typeof Worker`.
    ;(globalThis as unknown as { Worker: unknown }).Worker = class {}
  })

  afterEach(() => {
    delete (globalThis as unknown as { Worker?: unknown }).Worker
  })

  it('returns a plain (unhighlighted) placeholder synchronously and dispatches the highlight to the worker', () => {
    mockWorker.mockReturnValue(new Promise(() => {})) // never resolves: stay on the placeholder
    const html = renderMarkdown('```js\nconst x = 1\n```')
    // Placeholder renders the code as a real code BLOCK (a <pre> with the language
    // class) -- so it occupies the same height the highlighted result will, and the
    // copy-button injector finds a <pre> to augment -- but is NOT yet Shiki-highlighted
    // (no `pre.shiki`).
    expect(html).toContain('<pre')
    expect(html).toContain('language-js')
    expect(html).toContain('const x = 1')
    expect(html).not.toContain('class="shiki')
    expect(mockWorker).toHaveBeenCalledWith('```js\nconst x = 1\n```')
  })

  it('caches the worker result so a later (reactive) re-render returns the highlighted HTML', async () => {
    const text = '```js\nconst y = 2\n```'
    mockWorker.mockResolvedValue('<pre class="shiki">HIGHLIGHTED</pre>')
    const first = renderMarkdown(text)
    expect(first).not.toContain('shiki') // placeholder first
    await flushMicrotasks()
    // The completion filled the cache; a re-render now serves the highlighted HTML.
    expect(renderMarkdown(text)).toBe('<pre class="shiki">HIGHLIGHTED</pre>')
  })

  it('dispatches only once per distinct text while a render is in flight', () => {
    mockWorker.mockReturnValue(new Promise(() => {}))
    renderMarkdown('in flight once')
    renderMarkdown('in flight once')
    renderMarkdown('in flight once')
    expect(mockWorker).toHaveBeenCalledTimes(1)
  })

  it('skipCache returns a plain placeholder and does NOT dispatch a worker render (streaming)', () => {
    const html = renderMarkdown('```js\nconst z = 3\n```', true)
    expect(html).toContain('const z = 3')
    expect(html).not.toContain('class="shiki')
    expect(mockWorker).not.toHaveBeenCalled()
  })

  it('caches a plain render when the worker crashes (resolves null) -- no infinite re-dispatch', async () => {
    const text = 'crash recovery'
    mockWorker.mockResolvedValue(null)
    renderMarkdown(text)
    await flushMicrotasks()
    mockWorker.mockClear()
    // The null result cached the plain render, so a re-render is a cache hit, not a retry.
    expect(renderMarkdown(text)).toContain('crash recovery')
    expect(mockWorker).not.toHaveBeenCalled()
  })

  it('caches a plain render when the worker rejects -- no stuck placeholder or retry loop', async () => {
    const text = 'rejected worker'
    mockWorker.mockRejectedValue(new Error('worker crashed'))
    renderMarkdown(text)
    await flushMicrotasks()
    mockWorker.mockClear()
    expect(_getPlaceholderCacheSize()).toBe(0)
    expect(renderMarkdown(text)).toContain('rejected worker')
    expect(mockWorker).not.toHaveBeenCalled()
  })

  it('_resetMarkdownCache clears inFlight so the same text re-dispatches after a reset', () => {
    mockWorker.mockReturnValue(new Promise(() => {})) // never resolves -> stays in flight
    renderMarkdown('stuck in flight')
    expect(mockWorker).toHaveBeenCalledTimes(1)
    // Without clearing inFlight, the dedup guard would skip the dispatch forever, so a
    // clear-and-retry could never actually retry.
    _resetMarkdownCache()
    renderMarkdown('stuck in flight')
    expect(mockWorker).toHaveBeenCalledTimes(2)
  })

  it('skipCache (streaming) does not populate the placeholder cache', () => {
    mockWorker.mockReturnValue(new Promise(() => {}))
    renderMarkdown('streaming frame', true)
    // Streaming feeds a new distinct text every frame; caching each would churn the
    // placeholder cache the on-screen worker-pending bodies rely on, so skipCache renders
    // uncached.
    expect(_getPlaceholderCacheSize()).toBe(0)
    // A normal (non-skipCache) render DOES cache its placeholder while the worker runs.
    renderMarkdown('a real body')
    expect(_getPlaceholderCacheSize()).toBe(1)
  })
})
