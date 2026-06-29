import type { CachedToken } from './tokenCache'
import { createLazyOnigurumaHighlighter, resolveBundledLang } from './shikiLazyHighlighter'
import { DUAL_THEME_TOKEN_OPTIONS } from './shikiThemes'
import { toCachedTokens } from './tokenCache'

export interface TokenizeRequest {
  type: 'tokenize'
  id: number
  lang: string
  code: string
}

export interface TokenizeResponse {
  type: 'tokenize-result'
  id: number
  tokens: CachedToken[][] | null
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
    const lang = resolveBundledLang(msg.lang)
    // Unknown id (or a built-in like `ansi`, which has no bundled grammar and is
    // tokenized on the main thread): respond null so the renderer shows plain text.
    // A transient load 'failed' also responds null; the client never caches a null
    // result, so a later re-mount re-dispatches and recovers (its own retry policy).
    if (!lang || (await hl.ensureLanguage(lang)) !== 'loaded') {
      respondPlain()
      return
    }
    const result = hl.getHighlighter()!.codeToTokens(msg.code, { lang, ...DUAL_THEME_TOKEN_OPTIONS })
    const tokens: CachedToken[][] = toCachedTokens(result.tokens)
    const response: TokenizeResponse = { type: 'tokenize-result', id: msg.id, tokens }
    globalThis.postMessage(response)
  }
  catch {
    respondPlain()
  }
}
