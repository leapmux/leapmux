import type { MarkdownRenderRequest, MarkdownRenderResponse } from './markdownWorker'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MARKDOWN_SOURCE_FALLBACK_ATTRIBUTE } from './markdownSourceFallback'
import { syntaxThemePair } from './shikiThemes'

// A body whose processing raises the RangeError of a stack overflow (see the test).
const OVERFLOWING_SOURCE = '> overflowing <b>deep</b>'

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

  // The pipeline recurses once per blockquote level. In a worker thread it renders
  // 9,000 levels in about 2.4 s and overflows at 12,000 after about 2 s, and the
  // depth that overflows depends on the stack of the thread. A real overflow here
  // once ran past the 30 s test timeout under the load of the full suite, so every
  // processor raises the error that the overflow raises instead.
  it('answers a body that overflows the parser with its escaped source, processing it once', async () => {
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    const { handle, responses } = await loadWorker()
    // The worker's own unified instance: loadWorker reset the modules, and this
    // import reads the fresh one that it loaded. Its processors share one prototype.
    const { plainMarkdownProcessor } = await import('./markdownProcessor')
    const processorPrototype = Object.getPrototypeOf(plainMarkdownProcessor) as { processSync: (value: string) => unknown }
    const process = processorPrototype.processSync
    const processSync = vi.spyOn(processorPrototype, 'processSync').mockImplementation(function (this: unknown, value: string) {
      if (value === OVERFLOWING_SOURCE)
        throw new RangeError('Maximum call stack size exceeded')
      return process.call(this, value)
    })

    await handle(renderRequest(7, OVERFLOWING_SOURCE))

    // A reply that never came would leave the row that waits for it hidden forever.
    expect(responses).toHaveLength(1)
    expect(responses[0]).toMatchObject({ type: 'render-result', id: 7, retryable: false })
    expect(responses[0]?.html).toContain(MARKDOWN_SOURCE_FALLBACK_ATTRIBUTE)
    expect(responses[0]?.html).not.toContain('<b>')
    // The plain processor shares the parser, so a second pass could only fail again.
    expect(processSync.mock.calls.filter(([value]) => value === OVERFLOWING_SOURCE)).toHaveLength(1)
  })

  it('answers, retryable, when the highlighter cannot start and the plain parse fails too', async () => {
    // This path used to call the plain processor bare: its throw rejected the async
    // handler, `Worker.onerror` never saw it, and the request never got a reply.
    highlighterState.initFails = true
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    const { handle, responses } = await loadWorker()
    // The worker's own instance: loadWorker reset the modules, and this import reads
    // the fresh one that it loaded.
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
