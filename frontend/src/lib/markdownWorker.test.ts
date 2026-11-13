import type { MarkdownRenderRequest, MarkdownRenderResponse } from './markdownWorker'
import type { SyntaxThemePair } from './syntaxThemes'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { raiseStackOverflowFor } from '~/test-support/markdownParseOverflow'
import { MARKDOWN_SOURCE_FALLBACK_ATTRIBUTE } from './markdownSourceFallback'
import { syntaxThemePair } from './shikiThemes'
import { syntaxPairFor } from './syntaxThemes'

// A body whose processing raises the RangeError of a stack overflow (see the test).
const OVERFLOWING_SOURCE = '> overflowing <b>deep</b>'

/** A grammar load that reports its start and then waits until the test releases it. */
interface HeldGrammarLoad {
  started: () => void
  released: Promise<void>
}

/**
 * How the worker's highlighter behaves. `initFails` is read when the worker module
 * loads, and `heldGrammarLoad` and `grammarLoadFails` when a render loads a grammar.
 */
const highlighterState = vi.hoisted(() => ({
  initFails: false,
  heldGrammarLoad: undefined as HeldGrammarLoad | undefined,
  grammarLoadFails: false,
  /** The pair of the isolate each time a render starts the highlighter. */
  pairsAtReady: [] as SyntaxThemePair[],
}))

// ONE registration: the real highlighter, or one whose WASM engine never starts.
vi.mock('./shikiLazyHighlighter', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./shikiLazyHighlighter')>()
  const initFailure = () => Promise.reject(new Error('the WASM engine failed to load'))
  return {
    ...actual,
    createLazyOnigurumaHighlighter: () => {
      if (highlighterState.initFails) {
        return {
          ensureReady: initFailure,
          ensureThemes: initFailure,
          ensureLanguage: () => Promise.resolve('failed' as const),
        }
      }
      const highlighter = actual.createLazyOnigurumaHighlighter()
      return {
        ...highlighter,
        ensureReady: async () => {
          // The theme module of the current module graph. The worker module that
          // loadWorker loads fresh imports this same instance.
          const themes = await import('./shikiThemes')
          highlighterState.pairsAtReady.push(themes.syntaxThemePair())
          return highlighter.ensureReady()
        },
        ensureLanguage: async (lang: string) => {
          const held = highlighterState.heldGrammarLoad
          if (held !== undefined) {
            held.started()
            await held.released
          }
          // The result of a grammar chunk whose import throws.
          if (highlighterState.grammarLoadFails)
            return 'failed' as const
          return highlighter.ensureLanguage(lang)
        },
      }
    },
  }
})

/** Load the worker module fresh, so that it installs its handler on this test's globals. */
async function loadWorker(): Promise<{
  handle: (request: MarkdownRenderRequest) => Promise<void> | undefined
  responses: MarkdownRenderResponse[]
  /** The pair of the worker's isolate: the state of its own theme module. */
  isolatePair: () => SyntaxThemePair
}> {
  const responses: MarkdownRenderResponse[] = []
  vi.stubGlobal('postMessage', (response: MarkdownRenderResponse) => {
    responses.push(response)
  })
  vi.resetModules()
  await import('./markdownWorker')
  const handler = globalThis.onmessage as unknown as (event: MessageEvent<MarkdownRenderRequest>) => Promise<void> | undefined
  const themes = await import('./shikiThemes')
  return {
    handle: request => handler({ data: request } as MessageEvent<MarkdownRenderRequest>),
    responses,
    isolatePair: themes.syntaxThemePair,
  }
}

function renderRequest(id: number, text: string, syntax: SyntaxThemePair = syntaxThemePair()): MarkdownRenderRequest {
  return {
    type: 'render',
    id,
    text,
    syntax,
  }
}

describe('markdownWorker', () => {
  afterEach(() => {
    highlighterState.initFails = false
    highlighterState.heldGrammarLoad = undefined
    highlighterState.grammarLoadFails = false
    highlighterState.pairsAtReady = []
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
  // can run past the 30 s test timeout under the load of the full suite, so every
  // processor raises the error that the overflow raises instead.
  it('answers a body that overflows the parser with its escaped source, processing it once', async () => {
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    const { handle, responses } = await loadWorker()
    // The worker's own unified instance: loadWorker reset the modules, and this
    // import reads the fresh one that it loaded. Its processors share one prototype.
    const { plainMarkdownProcessor } = await import('./markdownProcessor')
    const processSync = raiseStackOverflowFor(plainMarkdownProcessor, OVERFLOWING_SOURCE)

    await handle(renderRequest(7, OVERFLOWING_SOURCE))

    // A reply that never came would leave the row that waits for it hidden forever.
    expect(responses).toHaveLength(1)
    expect(responses[0]).toMatchObject({ type: 'render-result', id: 7, retryable: false })
    expect(responses[0]?.html).toContain(MARKDOWN_SOURCE_FALLBACK_ATTRIBUTE)
    expect(responses[0]?.html).not.toContain('<b>')
    // The plain processor shares the parser, so a second pass could only fail again.
    expect(processSync.mock.calls.filter(([value]) => value === OVERFLOWING_SOURCE)).toHaveLength(1)
  })

  it('answers with the escaped source, not retryable, when the highlighter cannot start and the parser overflows', async () => {
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
    // Every processor shares the parser, so a retry after the highlighter starts
    // would hide the row through the same failed parse again.
    expect(responses[0]).toMatchObject({ type: 'render-result', id: 9, retryable: false })
    expect(responses[0]?.html).toContain(MARKDOWN_SOURCE_FALLBACK_ATTRIBUTE)
  })

  it('answers with the escaped source, not retryable, when a grammar load fails and the parser overflows', async () => {
    highlighterState.grammarLoadFails = true
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    const { handle, responses } = await loadWorker()
    const { plainMarkdownProcessor } = await import('./markdownProcessor')
    const text = '```js\nx\n```\n> deep'
    raiseStackOverflowFor(plainMarkdownProcessor, text)

    await handle(renderRequest(12, text))

    expect(responses).toHaveLength(1)
    // The failed grammar load alone would make the render retryable. The parse
    // failure overrides it, because a retry after the grammar loads fails the same way.
    expect(responses[0]).toMatchObject({ type: 'render-result', id: 12, retryable: false })
    expect(responses[0]?.html).toContain(MARKDOWN_SOURCE_FALLBACK_ATTRIBUTE)
  })

  it('answers, retryable, when a grammar load fails and the body parses', async () => {
    highlighterState.grammarLoadFails = true
    const { handle, responses } = await loadWorker()

    await handle(renderRequest(13, '```js\nx\n```\n**bold**'))

    expect(responses).toHaveLength(1)
    expect(responses[0]).toMatchObject({ type: 'render-result', id: 13, retryable: true })
    expect(responses[0]?.html).toContain('<strong>bold</strong>')
    expect(responses[0]?.html).not.toContain(MARKDOWN_SOURCE_FALLBACK_ATTRIBUTE)
  })

  // The client times the request at the front of its queue (see createWorkerClient),
  // so a request must not start while an earlier one waits. Otherwise the earlier
  // request is charged for the parse of the later one, and fails in its place.
  it('answers requests in arrival order while the first one waits for its grammar', async () => {
    let started: () => void = () => {}
    const firstWaits = new Promise<void>((resolve) => {
      started = resolve
    })
    let release: () => void = () => {}
    const released = new Promise<void>((resolve) => {
      release = resolve
    })
    highlighterState.heldGrammarLoad = { started, released }
    const { handle, responses } = await loadWorker()

    const first = handle(renderRequest(1, '```json\n{"a": 1}\n```'))
    await firstWaits
    const second = handle(renderRequest(2, 'a body with no fence'))
    // The highlighter is ready, so a body with no fence needs only microtasks to
    // finish. One macrotask turn drains them all: it finishes here if it may start.
    await new Promise(resolve => setTimeout(resolve, 0))
    expect(responses).toEqual([])

    release()
    await Promise.all([first, second])

    expect(responses.map(response => response.id)).toEqual([1, 2])
    expect(responses[0]?.html).toContain('class="shiki')
    expect(responses[1]?.html).toContain('a body with no fence')
  })

  it('answers the requests behind one whose reply cannot be posted', async () => {
    const error = vi.spyOn(console, 'error').mockImplementation(() => {})
    const { handle, responses } = await loadWorker()
    let posts = 0
    vi.stubGlobal('postMessage', (response: MarkdownRenderResponse) => {
      posts += 1
      if (posts === 1)
        throw new Error('the port is closed')
      responses.push(response)
    })

    const first = handle(renderRequest(1, 'first body'))
    const second = handle(renderRequest(2, 'second body'))

    await expect(first).rejects.toThrow('the port is closed')
    await second
    expect(responses.map(response => response.id)).toEqual([2])
    expect(error).toHaveBeenCalledOnce()
  })

  // The first-time init of the highlighter registers the pair of its isolate. A
  // pair adopted after the init would make a fresh worker load the themes of a
  // pair that no request asked for, and wait for them before its first render.
  it('adopts the pair of a request as the pair of its isolate before the highlighter starts', async () => {
    const nord = syntaxPairFor('nord')
    const { handle, responses, isolatePair } = await loadWorker()

    await handle(renderRequest(14, '```ts\nconst a = 1\n```', nord))

    expect(highlighterState.pairsAtReady).toEqual([nord])
    expect(isolatePair()).toEqual(nord)
    expect(responses).toHaveLength(1)
    expect(responses[0]).toMatchObject({ type: 'render-result', id: 14, retryable: false })
    expect(responses[0]?.html).toContain('class="shiki')
  })

  it('ignores a message that is not a render request', async () => {
    const { handle, responses, isolatePair } = await loadWorker()
    const before = isolatePair()

    expect(handle({
      type: 'tokenize',
      id: 15,
      text: 'x',
      syntax: syntaxPairFor('nord'),
    } as unknown as MarkdownRenderRequest)).toBeUndefined()
    // A render after it still gets its reply.
    await handle(renderRequest(16, 'next body'))

    expect(responses.map(response => response.id)).toEqual([16])
    expect(isolatePair()).toBe(before)
  })

  it('renders plain Markdown, retryable, when the highlighter cannot start', async () => {
    highlighterState.initFails = true
    const { handle, responses } = await loadWorker()

    await handle(renderRequest(11, '**bold**'))

    expect(responses[0]).toMatchObject({ type: 'render-result', id: 11, retryable: true })
    expect(responses[0]?.html).toContain('<strong>bold</strong>')
  })
})
