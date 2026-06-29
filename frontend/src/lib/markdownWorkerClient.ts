import type { MarkdownRenderRequest, MarkdownRenderResponse } from './markdownWorker'

// ---------------------------------------------------------------------------
// Markdown render worker client
//
// Promise-based bridge to markdownWorker (mirrors shikiWorkerClient). Lazily spawns
// the worker on first use, resolves each render by id, and on a worker crash
// resolves all pending requests to null (the caller keeps its plain placeholder)
// and drops the worker so the next call respawns it.
// ---------------------------------------------------------------------------

let worker: Worker | null = null
let nextId = 0
const pending = new Map<number, (html: string | null) => void>()

function failWorker(failedWorker: Worker | null): void {
  failedWorker?.terminate()
  if (worker !== failedWorker)
    return
  for (const resolve of pending.values())
    resolve(null)
  pending.clear()
  worker = null
}

function getWorker(): Worker {
  if (!worker) {
    const nextWorker = new Worker(
      new URL('./markdownWorker.ts', import.meta.url),
      { type: 'module' },
    )
    worker = nextWorker
    nextWorker.onmessage = (e: MessageEvent<MarkdownRenderResponse>) => {
      const { id, html } = e.data
      const resolve = pending.get(id)
      if (resolve) {
        pending.delete(id)
        resolve(html)
      }
    }
    nextWorker.onerror = () => {
      // On worker crash, resolve all pending to null (callers keep their plain
      // placeholder) and recreate the worker on the next call. Terminate the dead
      // worker first so its thread + Shiki highlighter aren't leaked across crashes.
      failWorker(nextWorker)
    }
  }
  return worker
}

/**
 * Render markdown to highlighted HTML off the main thread. Resolves to the HTML
 * string, or null if the worker crashed (the caller keeps its plain placeholder).
 */
export function renderMarkdownInWorker(text: string): Promise<string | null> {
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
    pending.set(id, resolve)
    const msg: MarkdownRenderRequest = { type: 'render', id, text }
    try {
      w.postMessage(msg)
    }
    catch {
      failWorker(w)
    }
  })
}
