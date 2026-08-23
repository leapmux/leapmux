import { sweepArtifacts } from './renderArtifactStore'
import { syntaxThemePair } from './shikiThemes'

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
//
// The worker CLIENTS are imported dynamically inside warmUpNow — a static
// import here would pull shikiWorkerClient onto entry-client's modulepreload
// graph and undo the critical-path cut.
// ---------------------------------------------------------------------------

/** Fallback delay when requestIdleCallback is unavailable (Safari). */
export const WARMUP_FALLBACK_DELAY_MS = 1500

/**
 * Delay on a constrained link (Save-Data, 2g/3g, or a phone-width viewport).
 * The warm-up pulls multi-megabyte worker chunks; starting it 1.5–5s after
 * mount on mobile LTE competed with the shell's remaining work. Twenty
 * seconds leaves first paint alone; the first code block still cold-starts
 * if the user reaches one sooner.
 */
export const WARMUP_CONSTRAINED_DELAY_MS = 20_000

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

/**
 * True when a warm-up download would steal bandwidth from first paint:
 * Save-Data, a slow effectiveType, or a mobile-layout viewport.
 */
export function isConstrainedStartupNetwork(): boolean {
  if (typeof navigator === 'undefined' || typeof window === 'undefined')
    return false
  const conn = (navigator as Navigator & {
    connection?: { saveData?: boolean, effectiveType?: string }
  }).connection
  if (conn?.saveData)
    return true
  const type = conn?.effectiveType
  if (type === 'slow-2g' || type === '2g' || type === '3g')
    return true
  // `md` breakpoint (768): matches useIsMobileLayout without importing Solid.
  if (window.matchMedia('(max-width: 767px)').matches)
    return true
  return false
}

function warmUpNow(): void {
  // Dynamic imports keep the worker bridges off the entry-client graph.
  // Results are discarded (markdown) or cached harmlessly (tokens); both calls
  // resolve null gracefully if a worker can't spawn.
  void import('./markdownWorkerClient').then(({ renderMarkdownInWorker }) => {
    void renderMarkdownInWorker(WARMUP_MARKDOWN, syntaxThemePair())
  })
  void import('./shikiWorkerClient').then(({ tokenizeAsync }) => {
    void tokenizeAsync(WARMUP_CODE_LANG, WARMUP_CODE)
  })
  void sweepArtifacts()
}

/**
 * Schedule the one-shot warm-up at browser idle. Safe to call from any client
 * entry point; repeat calls and non-browser environments (SSR, jsdom without
 * Worker) are no-ops.
 *
 * On a constrained network the warm-up is delayed rather than idle-forced, so
 * multi-megabyte worker fetches do not compete with shell hydration on LTE.
 */
export function scheduleRenderPipelineWarmup(): void {
  if (scheduled || typeof window === 'undefined' || typeof Worker === 'undefined')
    return
  scheduled = true
  if (isConstrainedStartupNetwork()) {
    setTimeout(warmUpNow, WARMUP_CONSTRAINED_DELAY_MS)
    return
  }
  if (typeof window.requestIdleCallback === 'function')
    window.requestIdleCallback(() => warmUpNow(), { timeout: WARMUP_IDLE_TIMEOUT_MS })
  else
    setTimeout(warmUpNow, WARMUP_FALLBACK_DELAY_MS)
}
