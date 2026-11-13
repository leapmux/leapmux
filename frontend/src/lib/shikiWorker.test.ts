import type { TokenizeRequest, TokenizeResponse } from './shikiWorker'
import type { SyntaxThemePair } from './syntaxThemes'
import type { InternedTokenLines } from './tokenCache'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { syntaxThemePair } from './shikiThemes'
import { syntaxPairFor } from './syntaxThemes'

/**
 * How the worker's highlighter fails, if at all. Each flag is read when a request
 * reaches the step that it breaks.
 */
const highlighterState = vi.hoisted(() => ({
  grammarLoadFails: false,
  themeLoadFails: false,
  tokenizeThrows: false,
  /**
   * The pair of the isolate each time a request starts to load its themes. The
   * first such load starts the first-time init, which registers that pair.
   */
  pairsAtThemeLoad: [] as SyntaxThemePair[],
}))

// The real highlighter, with a switch for each failure that a request can meet.
vi.mock('./shikiLazyHighlighter', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./shikiLazyHighlighter')>()
  return {
    ...actual,
    createLazyOnigurumaHighlighter: () => {
      const highlighter = actual.createLazyOnigurumaHighlighter()
      return {
        ...highlighter,
        ensureThemes: async (pair: SyntaxThemePair) => {
          // The theme module of the current module graph. A worker module that a
          // test loads fresh imports this same instance.
          const themes = await import('./shikiThemes')
          highlighterState.pairsAtThemeLoad.push(themes.syntaxThemePair())
          if (highlighterState.themeLoadFails)
            throw new Error('the theme chunk failed to load')
          return highlighter.ensureThemes(pair)
        },
        // The result of a grammar chunk whose import throws.
        ensureLanguage: async (lang: string) => (highlighterState.grammarLoadFails
          ? 'failed' as const
          : highlighter.ensureLanguage(lang)),
        getHighlighter: () => {
          const core = highlighter.getHighlighter()
          if (core === null || !highlighterState.tokenizeThrows)
            return core
          return {
            ...core,
            codeToTokens: () => {
              throw new Error('the grammar threw while it tokenized')
            },
          }
        },
      }
    },
  }
})

const { answerTokenizeRequest } = await import('./shikiWorker')

/** A pair other than the one that the page starts with. */
const NORD = syntaxPairFor('nord')

function tokenizeRequest(id: number, lang: string, code: string, syntax: SyntaxThemePair = syntaxThemePair()): TokenizeRequest {
  return {
    type: 'tokenize',
    id,
    lang,
    code,
    syntax,
  }
}

/** The text of each line of `tokens`, as the client draws it. */
function lineTexts(tokens: InternedTokenLines): string[] {
  return tokens.lines.map(line => line.map(([, content]) => content).join(''))
}

/** Load the worker module fresh, so that its message handler and its theme module are this test's own. */
async function loadWorker(): Promise<{
  handle: (data: unknown) => Promise<void>
  responses: TokenizeResponse[]
  isolatePair: () => SyntaxThemePair
}> {
  const responses: TokenizeResponse[] = []
  vi.stubGlobal('postMessage', (response: TokenizeResponse) => {
    responses.push(response)
  })
  vi.resetModules()
  await import('./shikiWorker')
  const handler = globalThis.onmessage as unknown as (event: MessageEvent) => Promise<void>
  const themes = await import('./shikiThemes')
  return {
    handle: data => handler({ data } as MessageEvent),
    responses,
    isolatePair: themes.syntaxThemePair,
  }
}

afterEach(() => {
  highlighterState.grammarLoadFails = false
  highlighterState.themeLoadFails = false
  highlighterState.tokenizeThrows = false
  highlighterState.pairsAtThemeLoad = []
  vi.unstubAllGlobals()
  globalThis.onmessage = null
})

describe('answerTokenizeRequest', () => {
  it('answers the tokens of a code in a bundled language, with the id of the request', async () => {
    const response = await answerTokenizeRequest(tokenizeRequest(4, 'typescript', 'const answer = 42\nlet x'))

    expect(response.type).toBe('tokenize-result')
    expect(response.id).toBe(4)
    expect(response.tokens).not.toBeNull()
    expect(lineTexts(response.tokens!)).toEqual(['const answer = 42', 'let x'])
    // Every style is the dual-theme pair of CSS variables that the client turns into a class.
    expect(response.tokens!.styles.length).toBeGreaterThan(0)
    for (const style of response.tokens!.styles)
      expect(JSON.stringify(style)).toContain('--shiki-light')
  })

  it('resolves an alias of a language, in any case', async () => {
    const response = await answerTokenizeRequest(tokenizeRequest(5, 'TS', 'const a = 1'))

    expect(response.tokens).not.toBeNull()
    expect(lineTexts(response.tokens!)).toEqual(['const a = 1'])
  })

  it('tokenizes an empty code', async () => {
    const response = await answerTokenizeRequest(tokenizeRequest(6, 'typescript', ''))

    expect(response.tokens).not.toBeNull()
    expect(lineTexts(response.tokens!)).toEqual([''])
  })

  it.each([
    ['a language that Shiki does not bundle', 'definitely-not-a-language'],
    ['the ANSI language, which the main thread colours', 'ansi'],
    ['an empty language', ''],
    ['a key of Object.prototype', 'constructor'],
  ])('answers null tokens for %s', async (_label, lang) => {
    const response = await answerTokenizeRequest(tokenizeRequest(7, lang, 'some code'))

    expect(response).toEqual({ type: 'tokenize-result', id: 7, tokens: null })
  })

  it.each([
    ['a grammar that fails to load', 'grammarLoadFails'],
    ['a theme that fails to load', 'themeLoadFails'],
    ['a grammar that throws while it tokenizes', 'tokenizeThrows'],
  ] as const)('answers null tokens, and no error, for %s', async (_label, failure) => {
    highlighterState[failure] = true

    const response = await answerTokenizeRequest(tokenizeRequest(8, 'typescript', 'const a = 1'))

    expect(response).toEqual({ type: 'tokenize-result', id: 8, tokens: null })
  })

  // A message that arrives while another one waits for a theme must not decide
  // its colours, so the pair travels as an argument. The handler adopts the pair
  // as the pair of its isolate; this function must not, because the unit tests
  // run it on the page's thread.
  it('tokenizes under the pair of the request, and leaves the pair of the page alone', async () => {
    const pageKey = JSON.stringify(syntaxThemePair())
    const code = 'const answer = 42'

    const underPage = await answerTokenizeRequest(tokenizeRequest(9, 'typescript', code))
    const underNord = await answerTokenizeRequest(tokenizeRequest(10, 'typescript', code, NORD))

    expect(JSON.stringify(syntaxThemePair())).toBe(pageKey)
    expect(lineTexts(underNord.tokens!)).toEqual([code])
    expect(underNord.tokens!.styles).not.toEqual(underPage.tokens!.styles)
  })
})

describe('shikiWorker message handler', () => {
  it('adopts the pair of a request as the pair of its isolate before the themes load, and posts the reply', async () => {
    const { handle, responses, isolatePair } = await loadWorker()

    await handle(tokenizeRequest(11, 'typescript', 'let x', NORD))

    // The first-time init registers the pair of the isolate, so a pair adopted
    // after it would load the themes of a pair that no request asked for.
    expect(highlighterState.pairsAtThemeLoad).toEqual([NORD])
    expect(isolatePair()).toEqual(NORD)
    expect(responses).toHaveLength(1)
    expect(responses[0]).toMatchObject({ type: 'tokenize-result', id: 11 })
    expect(lineTexts(responses[0]!.tokens!)).toEqual(['let x'])
  })

  it('posts the null tokens of a request that fails', async () => {
    const { handle, responses } = await loadWorker()

    await handle(tokenizeRequest(12, 'definitely-not-a-language', 'x'))

    expect(responses).toEqual([
      { type: 'tokenize-result', id: 12, tokens: null },
    ])
  })

  it('ignores a message that is not a tokenize request', async () => {
    const { handle, responses, isolatePair } = await loadWorker()
    const before = isolatePair()

    await handle({ type: 'render', id: 13, syntax: NORD })

    expect(responses).toEqual([])
    expect(highlighterState.pairsAtThemeLoad).toEqual([])
    expect(isolatePair()).toBe(before)
  })
})
