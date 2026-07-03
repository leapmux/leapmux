import { renderMarkdownInWorker } from './markdownWorkerClient'
import { sweepArtifacts } from './renderArtifactStore'
import { tokenizeAsync } from './shikiWorkerClient'

// ---------------------------------------------------------------------------
// Idle warm-start for the render pipeline
//
// Both render workers initialize lazily, so the FIRST visible code block used
// to pay the whole cold-start bill on the UI's critical path: spawning the
// worker (module fetch + eval), compiling the Oniguruma WASM engine, loading a
// grammar, and (for markdown) building the remark processor. Kicking one
// trivial job through each worker at idle moves all of that to a moment nobody
// is waiting on. The same idle slot runs the persisted-artifact sweep (TTL +
// entry cap), which wants exactly one execution per session.
// ---------------------------------------------------------------------------

/** Fallback delay when requestIdleCallback is unavailable (Safari). */
export const WARMUP_FALLBACK_DELAY_MS = 1500

/** Upper bound before a pending idle callback is forced to run anyway. */
export const WARMUP_IDLE_TIMEOUT_MS = 5000

// A fenced block forces the full init chain in the markdown worker (engine +
// grammar load + processor); the bare snippet does the same for the token
// worker. TypeScript: a real, commonly-hit grammar — warming it doubles as
// pre-loading the likeliest first language.
const WARMUP_MARKDOWN = '```ts\nconst warm = 1\n```\n'
const WARMUP_CODE_LANG = 'typescript'
const WARMUP_CODE = 'const warm = 1'

let scheduled = false

/** Visible for testing. */
export function _resetWarmupForTest(): void {
  scheduled = false
}

function warmUpNow(): void {
  // Results are discarded (markdown) or cached harmlessly (tokens); both calls
  // resolve null gracefully if a worker can't spawn.
  void renderMarkdownInWorker(WARMUP_MARKDOWN)
  void tokenizeAsync(WARMUP_CODE_LANG, WARMUP_CODE)
  void sweepArtifacts()
}

/**
 * Schedule the one-shot warm-up at browser idle. Safe to call from any client
 * entry point; repeat calls and non-browser environments (SSR, jsdom without
 * Worker) are no-ops.
 */
export function scheduleRenderPipelineWarmup(): void {
  if (scheduled || typeof window === 'undefined' || typeof Worker === 'undefined')
    return
  scheduled = true
  if (typeof window.requestIdleCallback === 'function')
    window.requestIdleCallback(() => warmUpNow(), { timeout: WARMUP_IDLE_TIMEOUT_MS })
  else
    setTimeout(warmUpNow, WARMUP_FALLBACK_DELAY_MS)
}
