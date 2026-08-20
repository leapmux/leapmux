import type { SyntaxThemePair } from './syntaxThemes'
import type { InternedTokenLines } from './tokenCache'
import { createLazyOnigurumaHighlighter, resolveBundledLang } from './shikiLazyHighlighter'
import { dualThemeTokenOptions, setSyntaxThemePair } from './shikiThemes'
import { internTokenLines, mergeLineTokens } from './tokenCache'

export interface TokenizeRequest {
  type: 'tokenize'
  id: number
  lang: string
  code: string
  /**
   * The syntax theme pair to tokenize under, carried per REQUEST rather than
   * set by a handshake.
   *
   * A handshake would leave a window in which a request issued under the old
   * theme is answered under the new one, and the client would cache that answer
   * under the new theme's key. Per-request costs two short strings and removes
   * the race entirely.
   */
  syntax: SyntaxThemePair
}

export interface TokenizeResponse {
  type: 'tokenize-result'
  id: number
  /** Interned wire shape; the client expands it back to CachedToken[][]. */
  tokens: InternedTokenLines | null
}

// One Oniguruma-backed highlighter per worker thread. Grammars load lazily on
// first use (cached thereafter), so the worker boots without compiling 20
// grammars up front and can tokenize any of Shiki's ~332 bundled languages.
const hl = createLazyOnigurumaHighlighter()

globalThis.onmessage = async (e: MessageEvent<TokenizeRequest>) => {
  const msg = e.data
  if (msg.type !== 'tokenize')
    return
  // Single null-tokens responder (plain-text fallback), so the unknown-lang and
  // error paths can't drift in shape.
  const respondPlain = (): void => {
    const response: TokenizeResponse = { type: 'tokenize-result', id: msg.id, tokens: null }
    globalThis.postMessage(response)
  }
  try {
    // Adopt the request's pair BEFORE the first `ensureThemes`, which awaits
    // `ensureReady` inside. The lazy highlighter's first-time init registers
    // whatever pair this isolate currently names, and nothing else sets it
    // here -- so without this line every fresh worker fetched and parsed the
    // DEFAULT pair's two TextMate documents that it would then never tokenize
    // with, and awaited that import before the first block could highlight.
    // `~/lib/markdownWorker` adopts the pair in the same order and for the
    // same reason.
    //
    // The module state decides the INIT registration and nothing else. The
    // pair itself travels to `codeToTokens` as an ARGUMENT below, so a second
    // message arriving while this one is suspended on a theme or grammar
    // import cannot decide this request's colours.
    setSyntaxThemePair(msg.syntax)
    await hl.ensureThemes(msg.syntax)
    const lang = resolveBundledLang(msg.lang)
    // Unknown id (or a built-in like `ansi`, which has no bundled grammar and is
    // tokenized on the main thread): respond null so the renderer shows plain text.
    // A transient load 'failed' also responds null; the client never caches a null
    // result, so a later re-mount re-dispatches and recovers (its own retry policy).
    if (!lang || (await hl.ensureLanguage(lang)) !== 'loaded') {
      respondPlain()
      return
    }
    const result = hl.getHighlighter()!.codeToTokens(msg.code, { lang, ...dualThemeTokenOptions(msg.syntax) })
    // Merge whitespace-only and same-style neighbors BEFORE interning: Shiki
    // only applies these merges on its codeToHast path, so the raw codeToTokens
    // output carries one extra span per indented line (see mergeLineTokens).
    const tokens: InternedTokenLines = internTokenLines(mergeLineTokens(result.tokens))
    const response: TokenizeResponse = { type: 'tokenize-result', id: msg.id, tokens }
    globalThis.postMessage(response)
  }
  catch {
    respondPlain()
  }
}
