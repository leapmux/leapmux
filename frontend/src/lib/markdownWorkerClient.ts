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

function getWorker(): Worker {
  if (!worker) {
    worker = new Worker(
      new URL('./markdownWorker.ts', import.meta.url),
      { type: 'module' },
    )
    worker.onmessage = (e: MessageEvent<MarkdownRenderResponse>) => {
      const { id, html } = e.data
      const resolve = pending.get(id)
      if (resolve) {
        pending.delete(id)
        resolve(html)
      }
    }
    worker.onerror = () => {
      // On worker crash, resolve all pending to null (callers keep their plain
      // placeholder) and recreate the worker on the next call. Terminate the dead
      // worker first so its thread + Shiki highlighter aren't leaked across crashes.
      for (const resolve of pending.values())
        resolve(null)
      pending.clear()
      worker?.terminate()
      worker = null
    }
  }
  return worker
}

/**
 * Render markdown to highlighted HTML off the main thread. Resolves to the HTML
 * string, or null if the worker crashed (the caller keeps its plain placeholder).
 */
export function renderMarkdownInWorker(text: string): Promise<string | null> {
  const id = nextId++
  const w = getWorker()
  return new Promise((resolve) => {
    pending.set(id, resolve)
    const msg: MarkdownRenderRequest = { type: 'render', id, text }
    w.postMessage(msg)
  })
}
