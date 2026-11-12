import type { MarkdownRenderRequest, MarkdownRenderResponse } from './markdownWorker'
import type { SyntaxThemePair } from './syntaxThemes'
import { createWorkerClient } from './workerClient'
import { createWorkerPriorityGate } from './workerPriorityGate'

// ---------------------------------------------------------------------------
// Markdown render worker client
//
// Promise-based bridge to markdownWorker. The lazy worker lifecycle (spawn,
// dispatch-by-id, crash recovery -> resolve pending to null + respawn) lives in the
// shared createWorkerClient factory; this client maps the request/response shapes
// and picks one of two lanes by body size. Unlike shikiWorkerClient it adds no
// cache/coalescing layer -- renderMarkdown owns that dedup (its module-level
// `inFlight` map).
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

/**
 * Bodies longer than this many UTF-16 code units render in the LARGE lane: a
 * worker of their own. The parse alone costs about 1.5 ms per KB in WebKit and up
 * to 4.5 ms per KB in V8, and Shiki roughly triples it for code, so a 1 MB body
 * holds its worker for seconds. In the standard lane, every row behind that body
 * would wait as a skeleton. 32 KB keeps a standard-lane render near 100 ms or less.
 */
export const LARGE_MARKDOWN_BODY_CHARS = 32 * 1024

/** The time budget of every render, whatever its size: a cold worker's start fits in it. */
export const MARKDOWN_RENDER_BASE_BUDGET_MS = 10_000

/**
 * The budget added for each KB of source: about four times the slowest rate
 * measured on a desktop for a highlighted render, so a slow device still finishes
 * an ordinary body.
 */
export const MARKDOWN_RENDER_BUDGET_MS_PER_KB = 40

/**
 * The longest a worker may spend on one render (see WorkerRequestOptions). Deeply
 * nested Markdown makes the parser work for minutes before it overflows its stack:
 * 144 KB of nested emphasis took 320 s in V8. Past the budget, the render resolves
 * to null, and the caller shows the escaped source. A 1 MB body gets about 51 s.
 */
export function markdownRenderBudgetMs(text: string): number {
  return MARKDOWN_RENDER_BASE_BUDGET_MS + Math.ceil(text.length / 1024) * MARKDOWN_RENDER_BUDGET_MS_PER_KB
}

/** One worker and the priority gate in front of it. */
interface RenderLane {
  client: ReturnType<typeof createRenderClient>
  gate: ReturnType<typeof createWorkerPriorityGate>
}

function createRenderClient() {
  return createWorkerClient<MarkdownRenderRequest, MarkdownRenderResult | null>({
    spawn: () => new Worker(new URL('./markdownWorker.ts', import.meta.url), { type: 'module' }),
    extract: (data: MarkdownRenderResponse) => ({
      id: data.id,
      value: { html: data.html, retryable: data.retryable ?? false, styles: data.styles ?? {} },
    }),
    failureValue: null,
  })
}

// Each lane spawns its worker lazily on its first request, so the large lane costs
// nothing until a large body arrives. Each gate orders its own lane: viewport rows'
// renders preempt overscan rows' (see createWorkerPriorityGate). Without a gate a
// mount burst posts every body FIFO, and the visible rows wait behind offscreen ones.
const standardLane: RenderLane = {
  client: createRenderClient(),
  gate: createWorkerPriorityGate(),
}
const largeLane: RenderLane = {
  client: createRenderClient(),
  gate: createWorkerPriorityGate(),
}

/** The lane that renders `text`; see {@link LARGE_MARKDOWN_BODY_CHARS}. */
function laneFor(text: string): RenderLane {
  return text.length > LARGE_MARKDOWN_BODY_CHARS
    ? largeLane
    : standardLane
}

/**
 * Render markdown to highlighted HTML off the main thread. Resolves to the render
 * result (HTML + a `retryable` flag), or null if the worker crashed, cannot start,
 * or ran past the render's time budget (the caller shows the escaped source).
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
 * render behind currently-high ones in its lane — used for rows outside the
 * near-viewport band, which upgrade automatically once scrolled in.
 */
export function renderMarkdownInWorker(
  text: string,
  pair: SyntaxThemePair,
  isLowPriority?: () => boolean,
): Promise<MarkdownRenderResult | null> {
  if (typeof Worker === 'undefined')
    return Promise.resolve(null)
  const { client, gate } = laneFor(text)
  return gate.enqueue(() => client.request(id => ({
    type: 'render',
    id,
    text,
    syntax: pair,
  }), {
    timeBudgetMs: markdownRenderBudgetMs(text),
  }), isLowPriority)
}
