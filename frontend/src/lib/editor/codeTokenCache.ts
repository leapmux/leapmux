import type { CachedToken } from '~/lib/tokenCache'
import { lruGet } from '~/lib/mapLru'

/** The tokens of one code text, one array for each line. */
export type CodeTokens = readonly (readonly CachedToken[])[]

/**
 * The tokens that the token worker answered for the code blocks of an editor,
 * by syntax theme, language and text.
 *
 * Every keystroke in a code block makes a new text, so the cache limits the
 * total length of its keys rather than their count. A key holds the whole text,
 * and the tokens of a text take about 27 bytes for each character of code, so
 * the default limit of 256 Ki characters keeps about 7 MB of tokens. A limit on
 * the count would let 32 versions of one 1,300-line block hold 43 MB.
 *
 * The newest entry always stays, even when its key alone exceeds the limit: the
 * editor reads the tokens from here after the worker answers, and a block whose
 * answer the cache dropped at once would ask the worker again and again.
 */
export interface CodeTokenCache {
  /** The tokens of `key`, which become the most recently used entry. */
  get: (key: string) => CodeTokens | undefined
  /** Whether the cache holds `key`. The order of the entries does not change. */
  has: (key: string) => boolean
  /** Store the tokens of `key` as the most recently used entry. */
  set: (key: string, tokens: CodeTokens) => void
}

/** The key of `code` in `lang` under the syntax theme of `themeKey`. */
export function codeTokenKey(themeKey: string, lang: string, code: string): string {
  return `${themeKey}\u0000${lang}\u0000${code}`
}

/** 256 Ki characters of keys: about 7 MB of tokens. */
const DEFAULT_MAX_KEY_LENGTH = 256 * 1024

export function createCodeTokenCache(maxKeyLength = DEFAULT_MAX_KEY_LENGTH): CodeTokenCache {
  const entries = new Map<string, CodeTokens>()
  // The sum of the lengths of the keys in `entries`.
  let keyLength = 0

  return {
    get: key => lruGet(entries, key),
    has: key => entries.has(key),
    set: (key, tokens) => {
      if (entries.delete(key))
        keyLength -= key.length
      entries.set(key, tokens)
      keyLength += key.length
      // A Map iterates in insertion order, so the oldest entries come first and
      // the entry just set comes last.
      for (const oldest of entries.keys()) {
        if (keyLength <= maxKeyLength || oldest === key)
          break
        entries.delete(oldest)
        keyLength -= oldest.length
      }
    },
  }
}
