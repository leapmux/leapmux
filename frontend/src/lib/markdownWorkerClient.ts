import type { MarkdownRenderRequest, MarkdownRenderResponse } from './markdownWorker'
import { createWorkerClient } from './workerClient'

// ---------------------------------------------------------------------------
// Markdown render worker client
//
// Promise-based bridge to markdownWorker. The lazy worker lifecycle (spawn,
// dispatch-by-id, crash recovery -> resolve pending to null + respawn) lives in the
// shared createWorkerClient factory; this client just maps the request/response
// shapes. Unlike shikiWorkerClient it adds no cache/coalescing layer -- renderMarkdown
// owns that dedup (its module-level `inFlight` Set).
// ---------------------------------------------------------------------------

/** A completed worker render: the HTML plus whether it degraded transiently (retry may recover). */
export interface MarkdownRenderResult {
  html: string
  retryable: boolean
}

const client = createWorkerClient<MarkdownRenderRequest, MarkdownRenderResult | null>({
  spawn: () => new Worker(new URL('./markdownWorker.ts', import.meta.url), { type: 'module' }),
  extract: (data: MarkdownRenderResponse) => ({
    id: data.id,
    value: { html: data.html, retryable: data.retryable ?? false },
  }),
  failureValue: null,
})

/**
 * Render markdown to highlighted HTML off the main thread. Resolves to the render
 * result (HTML + a `retryable` flag), or null if the worker crashed (the caller
 * keeps its plain placeholder).
 */
export function renderMarkdownInWorker(text: string): Promise<MarkdownRenderResult | null> {
  if (typeof Worker === 'undefined')
    return Promise.resolve(null)
  return client.request(id => ({ type: 'render', id, text }))
}
