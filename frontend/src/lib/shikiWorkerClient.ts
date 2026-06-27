import type { TokenizeRequest, TokenizeResponse } from './shikiWorker'
import type { CachedToken } from './tokenCache'
import { getCachedTokens, setCachedTokens } from './tokenCache'

let worker: Worker | null = null
let nextId = 0
const pending = new Map<number, {
  resolve: (tokens: CachedToken[][] | null) => void
}>()

function failWorker(failedWorker: Worker | null): void {
  failedWorker?.terminate()
  if (worker !== failedWorker)
    return
  for (const entry of pending.values())
    entry.resolve(null)
  pending.clear()
  worker = null
}

function getWorker(): Worker {
  if (!worker) {
    const nextWorker = new Worker(
      new URL('./shikiWorker.ts', import.meta.url),
      { type: 'module' },
    )
    worker = nextWorker
    nextWorker.onmessage = (e: MessageEvent<TokenizeResponse>) => {
      const { id, tokens } = e.data
      const entry = pending.get(id)
      if (entry) {
        pending.delete(id)
        entry.resolve(tokens)
      }
    }
    nextWorker.onerror = () => {
      // On worker crash, reject all pending and recreate on next call. Terminate the
      // dead worker first so its thread + Shiki highlighter aren't leaked across crashes.
      failWorker(nextWorker)
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

  if (typeof Worker === 'undefined')
    return Promise.resolve(null)

  const id = nextId++
  let w: Worker
  try {
    w = getWorker()
  }
  catch {
    worker = null
    return Promise.resolve(null)
  }

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
    try {
      w.postMessage(msg)
    }
    catch {
      failWorker(w)
    }
  })
}
