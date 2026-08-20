import type { MarkdownRenderRequest, MarkdownRenderResponse } from './markdownWorker'
import type { SyntaxThemePair } from './syntaxThemes'
import { createWorkerClient } from './workerClient'
import { createWorkerPriorityGate } from './workerPriorityGate'

// ---------------------------------------------------------------------------
// Markdown render worker client
//
// Promise-based bridge to markdownWorker. The lazy worker lifecycle (spawn,
// dispatch-by-id, crash recovery -> resolve pending to null + respawn) lives in the
// shared createWorkerClient factory; this client just maps the request/response
// shapes. Unlike shikiWorkerClient it adds no cache/coalescing layer -- renderMarkdown
// owns that dedup (its module-level `inFlight` Set).
// ---------------------------------------------------------------------------

/**
 * A completed worker render: the HTML, whether it degraded transiently (retry
 * may recover), and the worker's className -> declaration dictionary for the
 * shared token-style classes the HTML references (the caller must inject the
 * rules via ensureShikiStyleRules before the HTML renders — see shikiStyleClass).
 */
export interface MarkdownRenderResult {
  html: string
  retryable: boolean
  styles: Record<string, string>
}

const client = createWorkerClient<MarkdownRenderRequest, MarkdownRenderResult | null>({
  spawn: () => new Worker(new URL('./markdownWorker.ts', import.meta.url), { type: 'module' }),
  extract: (data: MarkdownRenderResponse) => ({
    id: data.id,
    value: { html: data.html, retryable: data.retryable ?? false, styles: data.styles ?? {} },
  }),
  failureValue: null,
})

// Dispatch order gate: viewport rows' renders preempt overscan rows' (see
// createWorkerPriorityGate). Without it a mount burst posts every body FIFO
// and the visible rows' highlighted upgrades wait behind offscreen ones.
const gate = createWorkerPriorityGate()

/**
 * Render markdown to highlighted HTML off the main thread. Resolves to the render
 * result (HTML + a `retryable` flag), or null if the worker crashed (the caller
 * keeps its plain placeholder).
 *
 * `pair` IS A PARAMETER, and the caller captures it at dispatch beside the
 * artifact namespace it captures there. The gate holds all but a couple of jobs
 * client-side, which is the normal state during a mount burst, so a pair read
 * inside the enqueued thunk is the pair that is live when the gate releases the
 * job -- not the one the caller answered for. That sent the NEW theme's pair to
 * the worker while the reply was filed under the OLD theme's namespace, where a
 * later session read it back as valid and painted the abandoned theme's colours.
 * `tokenizeAsync` in `~/lib/shikiWorkerClient` carries its pair the same way.
 *
 * `isLowPriority` (re-read at each dispatch opportunity) deprioritizes this
 * render behind currently-high ones — used for rows outside the near-viewport
 * band, which upgrade automatically once scrolled in.
 */
export function renderMarkdownInWorker(
  text: string,
  pair: SyntaxThemePair,
  isLowPriority?: () => boolean,
): Promise<MarkdownRenderResult | null> {
  if (typeof Worker === 'undefined')
    return Promise.resolve(null)
  return gate.enqueue(() => client.request(id => ({ type: 'render', id, text, syntax: pair })), isLowPriority)
}
