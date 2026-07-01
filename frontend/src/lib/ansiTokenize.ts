import type { CachedToken } from './tokenCache'
import { shikiHighlighter } from './renderMarkdown'
import { DUAL_THEME_TOKEN_OPTIONS } from './shikiThemes'
import { toCachedTokens } from './tokenCache'

/**
 * Synchronous main-thread tokenizer for ANSI escape output, shaped as a
 * {@link useAsyncCodeTokens} `syncTokenize` option.
 *
 * `ansi` is a Shiki SPECIAL language, not a bundled TextMate grammar: the async
 * Oniguruma token worker can't produce it -- `resolveBundledLang('ansi')` is
 * undefined, so the worker short-circuits to a plain response. The synchronous
 * JS-engine `shikiHighlighter` DOES tokenize it (Shiki handles ANSI internally,
 * engine-independently), so every `useAsyncCodeTokens` consumer that can see an
 * `ansi` language passes this to keep ANSI colored on the main thread instead of
 * degrading to plain.
 *
 * `ansi` reaches those consumers via `guessLanguage` mapping `.log` files to it:
 * the Read view (a `.log` cat-n body) AND both diff sides + the diff gap-context
 * lines (a `.log` file's Edit/Write diff). Returns null for any non-`ansi` lang
 * (fall through to the worker) or when tokenization throws.
 */
export function ansiSyncTokenize(lang: string, code: string): CachedToken[][] | null {
  if (lang !== 'ansi')
    return null
  try {
    return toCachedTokens(shikiHighlighter.codeToTokens(code, { lang: 'ansi', ...DUAL_THEME_TOKEN_OPTIONS }).tokens)
  }
  catch {
    return null
  }
}
