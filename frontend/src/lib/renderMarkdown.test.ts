import { IDBFactory } from 'fake-indexeddb'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { _resetArtifactStoreForTest, getArtifact, putArtifact } from './renderArtifactStore'
import { _getMarkdownCacheSize, _resetMarkdownCache, MARKDOWN_ARTIFACT_NS, renderMarkdown } from './renderMarkdown'
import { _resetShikiStyleClassesForTest } from './shikiStyleClass'
import { readInjectedShikiRules } from './shikiStyleClass.testkit'

const originalWorkerDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'Worker')

function restoreWorker() {
  if (originalWorkerDescriptor) {
    Object.defineProperty(globalThis, 'Worker', originalWorkerDescriptor)
    return
  }
  Reflect.deleteProperty(globalThis, 'Worker')
}

interface CapturedWorker {
  onmessage: ((event: MessageEvent) => void) | null
  messages: Array<{ id: number, text: string }>
}

// Shared across the whole file: the worker CLIENT module caches its first
// spawned Worker instance, so a per-test array would miss messages posted to a
// worker spawned in an earlier test. Tests filter by their unique text instead.
const workers: CapturedWorker[] = []

function installCapturingWorker(): void {
  Object.defineProperty(globalThis, 'Worker', {
    configurable: true,
    writable: true,
    value: class CapturingWorker {
      onmessage: ((event: MessageEvent) => void) | null = null
      onerror: (() => void) | null = null
      messages: Array<{ id: number, text: string }> = []
      terminate = vi.fn()

      constructor() {
        workers.push(this)
      }

      postMessage(message: { id: number, text: string }) {
        this.messages.push(message)
      }
    },
  })
}

function dispatchesFor(text: string): Array<{ worker: CapturedWorker, id: number }> {
  return workers.flatMap(worker => worker.messages
    .filter(m => m.text === text)
    .map(m => ({ worker, id: m.id })))
}

function injectedRules(): string {
  return readInjectedShikiRules()
}

describe('rendermarkdown shared token-style classes', () => {
  beforeEach(() => {
    _resetMarkdownCache()
    _resetShikiStyleClassesForTest()
  })

  afterEach(() => {
    restoreWorker()
    vi.unstubAllGlobals()
    _resetArtifactStoreForTest()
  })

  it('renders fenced code with class-based token spans and injects the rules (sync path)', () => {
    // jsdom has no Worker, so this exercises the synchronous highlighted
    // fallback -- the same createMarkdownProcessor pipeline (and transformer)
    // the worker runs.
    const html = renderMarkdown('```js\nconst x = 1\n```')
    // Token spans carry shared classes, never inline styles. (The <pre> keeps
    // its per-block rootStyle -- one element -- so scope the assertion to spans.)
    expect(html).toContain('class="sk-')
    expect(html).not.toContain('<span style=')
    // Every class referenced by the HTML has an injected rule defining the
    // dual-theme variables.
    const classes = [...html.matchAll(/class="(sk-[0-9a-z-]+)"/g)].map(m => m[1])
    expect(classes.length).toBeGreaterThan(0)
    for (const className of new Set(classes))
      expect(injectedRules()).toContain(`.${className}{`)
    expect(injectedRules()).toContain('--shiki-light')
    expect(injectedRules()).toContain('--shiki-dark')
  })

  it('serves a persisted {html, styles} artifact and injects its rules without dispatching the worker', async () => {
    vi.stubGlobal('indexedDB', new IDBFactory())
    installCapturingWorker()
    const text = 'persisted warm start body'
    await putArtifact(MARKDOWN_ARTIFACT_NS, text, {
      h: '<p data-persisted="">persisted warm start body</p>',
      s: { 'sk-00000000-1s': '--shiki-light:#123;--shiki-dark:#456' },
    })

    // First call returns the plain placeholder and starts the async warm-start.
    expect(renderMarkdown(text)).not.toContain('data-persisted')

    await vi.waitFor(() => {
      expect(renderMarkdown(text)).toBe('<p data-persisted="">persisted warm start body</p>')
    })
    // The dictionary's rules were injected before the HTML could render.
    expect(injectedRules()).toContain('.sk-00000000-1s{--shiki-light:#123;--shiki-dark:#456}')
    // The warm-start never dispatched this text to a worker.
    expect(dispatchesFor(text)).toHaveLength(0)
  })

  it('rejects a legacy plain-string artifact and falls through to the worker', async () => {
    vi.stubGlobal('indexedDB', new IDBFactory())
    installCapturingWorker()
    const text = 'legacy artifact body'
    // The pre-{h,s} artifact shape: a bare HTML string.
    await putArtifact(MARKDOWN_ARTIFACT_NS, text, '<p>legacy</p>')

    renderMarkdown(text)

    // The malformed hit is a miss: the render dispatches to the worker.
    await vi.waitFor(() => {
      expect(dispatchesFor(text)).toHaveLength(1)
    })
    const [{ worker, id }] = dispatchesFor(text)
    worker.onmessage?.({
      data: { id, html: '<p>legacy fallback</p>', retryable: false, styles: {} },
    } as MessageEvent)
    await vi.waitFor(() => {
      expect(renderMarkdown(text)).toBe('<p>legacy fallback</p>')
    })
  })

  it('rejects an oversized persisted html artifact and falls through to the worker', async () => {
    vi.stubGlobal('indexedDB', new IDBFactory())
    installCapturingWorker()
    const text = 'oversized artifact body'
    await putArtifact(MARKDOWN_ARTIFACT_NS, text, {
      h: `<p>${'x'.repeat(512 * 1024)}</p>`,
      s: {},
    })

    renderMarkdown(text)

    await vi.waitFor(() => {
      expect(dispatchesFor(text)).toHaveLength(1)
    })
    const [{ worker, id }] = dispatchesFor(text)
    worker.onmessage?.({
      data: { id, html: '<p>oversized fallback</p>', retryable: false, styles: {} },
    } as MessageEvent)
    await vi.waitFor(() => {
      expect(renderMarkdown(text)).toBe('<p>oversized fallback</p>')
    })
  })

  it('injects a worker result\'s style dictionary and persists {html, styles}', async () => {
    vi.stubGlobal('indexedDB', new IDBFactory())
    installCapturingWorker()
    const text = 'worker highlighted body'

    renderMarkdown(text)
    await vi.waitFor(() => expect(dispatchesFor(text)).toHaveLength(1))

    const { worker, id } = dispatchesFor(text)[0]
    worker.onmessage?.({
      data: {
        id,
        html: '<pre class="shiki"><code><span class="sk-00000001-13">x</span></code></pre>',
        retryable: false,
        styles: { 'sk-00000001-13': '--shiki-light:#aaa' },
      },
    } as MessageEvent)

    await vi.waitFor(() => {
      expect(renderMarkdown(text)).toContain('sk-00000001-13')
    })
    // The worker has no document; the main thread injected the shipped rules.
    expect(injectedRules()).toContain('.sk-00000001-13{--shiki-light:#aaa}')
    // Persisted for the next session's warm start, dictionary included.
    await vi.waitFor(async () => {
      await expect(getArtifact(MARKDOWN_ARTIFACT_NS, text)).resolves.toEqual({
        h: '<pre class="shiki"><code><span class="sk-00000001-13">x</span></code></pre>',
        s: { 'sk-00000001-13': '--shiki-light:#aaa' },
      })
    })
  })
})

describe('rendermarkdown cache and gfm', () => {
  beforeEach(() => {
    _resetMarkdownCache()
  })

  it('should render markdown with syntax highlighting', () => {
    const html = renderMarkdown('```js\nconst x = 1\n```', true)
    expect(html).toContain('class="shiki')
    expect(html).toContain('const')
  })

  it('should render plain text', () => {
    const html = renderMarkdown('hello world', true)
    expect(html).toContain('hello world')
  })

  it('should render inline code', () => {
    const html = renderMarkdown('use `const x = 1`', true)
    expect(html).toContain('<code>')
    expect(html).toContain('const x = 1')
  })

  it('should render code blocks with unknown languages without crashing', () => {
    // Unknown language should fall back gracefully (no Shiki highlighting).
    const html = renderMarkdown('```unknownlang123\nfoo bar\n```', true)
    expect(html).toContain('foo bar')
  })

  it('should render GFM tables', () => {
    const md = '| a | b |\n|---|---|\n| 1 | 2 |'
    const html = renderMarkdown(md, true)
    expect(html).toContain('<table>')
    expect(html).toContain('<td>')
  })

  it('should cache results', () => {
    expect(_getMarkdownCacheSize()).toBe(0)
    const html1 = renderMarkdown('cached test')
    expect(_getMarkdownCacheSize()).toBe(1)
    const html2 = renderMarkdown('cached test')
    // Second call must hit the cache -- size stays at 1, not 2.
    expect(_getMarkdownCacheSize()).toBe(1)
    expect(html1).toBe(html2)
  })

  it('should bypass cache when skipCache is true', () => {
    expect(_getMarkdownCacheSize()).toBe(0)
    renderMarkdown('cached test', true)
    expect(_getMarkdownCacheSize()).toBe(0)
  })
})
