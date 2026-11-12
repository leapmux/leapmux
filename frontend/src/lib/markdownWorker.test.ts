import type { MarkdownRenderRequest, MarkdownRenderResponse } from './markdownWorker'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MARKDOWN_SOURCE_FALLBACK_ATTRIBUTE } from './markdownSourceFallback'
import { syntaxThemePair } from './shikiThemes'

// The parser recurses once per blockquote level, so nesting overflows its stack:
// at about 2,400 levels on a main thread and about 9,000 in a worker thread, whose
// stack is larger. The work before the overflow grows faster than the depth, so
// this depth stays near that bound, with a margin.
const OVERFLOW_DEPTH = 12_000
const OVERFLOWING_SOURCE = `${'>'.repeat(OVERFLOW_DEPTH)} <b>deep</b>`

/** Whether the worker's highlighter fails to start; read when the worker module loads. */
const highlighterState = vi.hoisted(() => ({ initFails: false }))

// ONE registration: the real highlighter, or one whose WASM engine never starts.
vi.mock('./shikiLazyHighlighter', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./shikiLazyHighlighter')>()
  const initFailure = () => Promise.reject(new Error('the WASM engine failed to load'))
  return {
    ...actual,
    createLazyOnigurumaHighlighter: () => highlighterState.initFails
      ? {
          ensureReady: initFailure,
          ensureThemes: initFailure,
          ensureLanguage: () => Promise.resolve('failed' as const),
          isLanguageLoaded: () => false,
        }
      : actual.createLazyOnigurumaHighlighter(),
  }
})

/** Load the worker module fresh, so that it installs its handler on this test's globals. */
async function loadWorker(): Promise<{
  handle: (request: MarkdownRenderRequest) => Promise<void>
  responses: MarkdownRenderResponse[]
}> {
  const responses: MarkdownRenderResponse[] = []
  vi.stubGlobal('postMessage', (response: MarkdownRenderResponse) => {
    responses.push(response)
  })
  vi.resetModules()
  await import('./markdownWorker')
  const handler = globalThis.onmessage as unknown as (event: MessageEvent<MarkdownRenderRequest>) => Promise<void>
  return {
    handle: request => handler({ data: request } as MessageEvent<MarkdownRenderRequest>),
    responses,
  }
}

function renderRequest(id: number, text: string): MarkdownRenderRequest {
  return {
    type: 'render',
    id,
    text,
    syntax: syntaxThemePair(),
  }
}

describe('markdownWorker', () => {
  afterEach(() => {
    highlighterState.initFails = false
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
    globalThis.onmessage = null
  })

  it('renders a body and answers with its id', async () => {
    const { handle, responses } = await loadWorker()

    await handle(renderRequest(3, '# Title'))

    expect(responses).toHaveLength(1)
    expect(responses[0]).toMatchObject({ type: 'render-result', id: 3, retryable: false })
    expect(responses[0]?.html).toContain('<h1>Title</h1>')
  })

  it('answers a body that overflows the parser with its escaped source', async () => {
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    const { handle, responses } = await loadWorker()

    await handle(renderRequest(7, OVERFLOWING_SOURCE))

    // A reply that never came would leave the row that waits for it hidden forever.
    expect(responses).toHaveLength(1)
    expect(responses[0]).toMatchObject({ type: 'render-result', id: 7, retryable: false })
    expect(responses[0]?.html).toContain(MARKDOWN_SOURCE_FALLBACK_ATTRIBUTE)
    expect(responses[0]?.html).not.toContain('<b>')
  })

  it('answers, retryable, when the highlighter cannot start and the plain parse fails too', async () => {
    // This path used to call the plain processor bare: its throw rejected the async
    // handler, `Worker.onerror` never saw it, and the request never got a reply.
    highlighterState.initFails = true
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    const { handle, responses } = await loadWorker()
    // The worker's own instance: loadWorker reset the modules, and this import reads
    // the fresh one that it loaded. The test above holds the one real overflow.
    const { plainMarkdownProcessor } = await import('./markdownProcessor')
    vi.spyOn(plainMarkdownProcessor, 'processSync').mockImplementation(() => {
      throw new RangeError('Maximum call stack size exceeded')
    })

    await handle(renderRequest(9, '> deep'))

    expect(responses).toHaveLength(1)
    expect(responses[0]).toMatchObject({ type: 'render-result', id: 9, retryable: true })
    expect(responses[0]?.html).toContain(MARKDOWN_SOURCE_FALLBACK_ATTRIBUTE)
  })

  it('renders plain Markdown, retryable, when the highlighter cannot start', async () => {
    highlighterState.initFails = true
    const { handle, responses } = await loadWorker()

    await handle(renderRequest(11, '**bold**'))

    expect(responses[0]).toMatchObject({ type: 'render-result', id: 11, retryable: true })
    expect(responses[0]?.html).toContain('<strong>bold</strong>')
  })
})
