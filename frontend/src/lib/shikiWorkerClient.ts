import type { TokenizeRequest, TokenizeResponse } from './shikiWorker'
import type { CachedToken } from './tokenCache'
import { getCachedTokens, setCachedTokens } from './tokenCache'

let worker: Worker | null = null
let nextId = 0
const pending = new Map<number, {
  resolve: (tokens: CachedToken[][] | null) => void
}>()

function getWorker(): Worker {
  if (!worker) {
    worker = new Worker(
      new URL('./shikiWorker.ts', import.meta.url),
      { type: 'module' },
    )
    worker.onmessage = (e: MessageEvent<TokenizeResponse>) => {
      const { id, tokens } = e.data
      const entry = pending.get(id)
      if (entry) {
        pending.delete(id)
        entry.resolve(tokens)
      }
    }
    worker.onerror = () => {
      // On worker crash, reject all pending and recreate on next call
      for (const entry of pending.values()) {
        entry.resolve(null)
      }
      pending.clear()
      worker = null
    }
  }
  return worker
}

/**
 * Tokenize code asynchronously via the Web Worker.
 * Checks the cache first and populates it on completion.
 */
export function tokenizeAsync(
  lang: string,
  code: string,
): Promise<CachedToken[][] | null> {
  const cached = getCachedTokens(lang, code)
  if (cached)
    return Promise.resolve(cached)

  const id = nextId++
  const w = getWorker()

  return new Promise((resolve) => {
    pending.set(id, {
      resolve: (tokens) => {
        if (tokens) {
          setCachedTokens(lang, code, tokens)
        }
        resolve(tokens)
      },
    })
    const msg: TokenizeRequest = { type: 'tokenize', id, lang, code }
    w.postMessage(msg)
  })
}
