import type { MarkdownRenderRequest, MarkdownRenderResponse } from './markdownWorker'
import type { SyntaxThemePair } from './syntaxThemes'
import { themeStillCurrent } from './shikiThemes'
import { createWorkerClient } from './workerClient'
import { createWorkerPriorityGate } from './workerPriorityGate'

// ---------------------------------------------------------------------------
// Markdown render worker client
//
// Promise-based bridge to markdownWorker. The lazy worker lifecycle (spawn,
// dispatch-by-id, crash recovery: the render in progress resolves to null, and the
// others go to a fresh worker) lives in the shared createWorkerClient factory; this client maps the request/response shapes
// and picks one of two lanes by body size. Unlike shikiWorkerClient it adds no
// cache/coalescing layer -- renderMarkdown owns that dedup (its module-level
// `inFlight` map).
// ---------------------------------------------------------------------------

/**
 * A completed worker render: the HTML, whether it degraded transiently (retry
 * may recover), and the className -> declaration dictionary of the token-style
 * classes that the worker minted for the first time in this render (see
 * MarkdownRenderResponse.styles). The HTML can also use classes that an earlier
 * render minted. The caller must inject the rules via ensureShikiStyleRules
 * before the HTML renders, and also when it discards the HTML, because the worker
 * never sends them again (see shikiStyleClass).
 */
export interface MarkdownRenderResult {
  html: string
  retryable: boolean
  styles: Record<string, string>
}

/**
 * Bodies longer than this many UTF-16 code units render in the LARGE lane: a
 * worker of their own. A highlighted render measured, in V8 on a desktop, about
 * 6 ms per KB for fenced code, and 2.5 ms per KB for prose at 32 KB, rising to
 * 4 ms per KB at 256 KB. So a 1 MB body holds its worker for seconds, and in the
 * standard lane every row behind that body would wait as a skeleton. At this
 * limit, a standard-lane render takes about 80 ms for prose and 200 ms for code.
 */
export const LARGE_MARKDOWN_BODY_CHARS = 32 * 1024

/**
 * The time budget of every render, whatever its size. A worker that has not
 * answered yet gets the factory's cold-start allowance on top of it, for its load
 * (see WorkerClientOptions.coldStartAllowanceMs).
 */
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
 * result (HTML + a `retryable` flag), or null if the worker crashed, cannot start
 * (also where no Worker exists), or ran past the render's time budget (the caller
 * shows the escaped source). Never rejects.
 *
 * It also resolves to null, and posts nothing, when `pair` is no longer the
 * current syntax theme at the moment the gate releases the job. That render could
 * only be discarded (see themeStillCurrent), and it would delay every render
 * behind it by its whole length. The caller abandoned it already: a theme change
 * clears the renders in flight and renders each body again under the new pair.
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
  const { client, gate } = laneFor(text)
  return gate.enqueue(() => {
    if (!themeStillCurrent(pair))
      return Promise.resolve(null)
    return client.request(id => ({
      type: 'render',
      id,
      text,
      syntax: pair,
    }), {
      timeBudgetMs: markdownRenderBudgetMs(text),
    })
  }, isLowPriority)
}
