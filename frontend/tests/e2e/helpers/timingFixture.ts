/**
 * Shared infrastructure for perf-timing e2e specs (e.g.
 * 121-claude-agent-open-timing, 122-tab-close-timing). Each spec uses
 * its own dev-server fixture so it can pass `LEAPMUX_TRACE_*` env vars
 * and capture backend stderr for slog phase markers. This module
 * consolidates the log-line buffer, JSON-line parsing, browser-side
 * RPC mark wiring, and the ASCII-table timeline renderer.
 *
 * The spec-specific bits (which phase names to expect, which markers
 * to filter, what DOM events to observe with MutationObserver) stay in
 * each spec file.
 */
import type { Page } from '@playwright/test'
import type { Buffer } from 'node:buffer'
import type { DevServerHandle } from './devServer'
import { startDevServer } from './devServer'

// ──────────────────────────────────────────────
// Types
// ──────────────────────────────────────────────

export interface LogLine {
  raw: string
  json: Record<string, unknown> | null
  rxAt: number
}

export interface PhaseMark {
  name: string
  tMs: number
}

export interface RpcMark {
  type: 'rpc-send' | 'rpc-recv'
  method: string
  at: number
  ok?: boolean
}

export interface TimingServer extends DevServerHandle {
  /** All backend log lines emitted since server start, parsed if JSON. */
  logLines: LogLine[]
}

// ──────────────────────────────────────────────
// Dev server with stderr capture
// ──────────────────────────────────────────────

/**
 * Parse a single JSON log line. Returns null on non-JSON or parse error.
 * slog's JSONHandler emits one JSON object per line on stderr when not
 * attached to a TTY (which is how child_process pipes work).
 */
function parseJsonLine(line: string): Record<string, unknown> | null {
  const trimmed = line.trim()
  if (!trimmed.startsWith('{'))
    return null
  try {
    return JSON.parse(trimmed) as Record<string, unknown>
  }
  catch {
    return null
  }
}

export interface TimingServerOptions {
  /** Prefix for the mkdtemp name (helps when debugging leftover dirs). */
  dataDirPrefix: string
  /**
   * Extra env vars layered on top of process.env. Pass the
   * spec-specific LEAPMUX_TRACE_* gate here.
   */
  env: Record<string, string>
}

/**
 * Spin up `leapmux dev` with stderr captured into `logLines`. Each log
 * line is parsed as JSON when possible; the raw text is preserved for
 * grep-style lookups.
 */
export async function startTimingServer(opts: TimingServerOptions): Promise<TimingServer> {
  const logLines: LogLine[] = []
  const handleChunk = (chunk: Buffer) => {
    const text = chunk.toString('utf8')
    const now = performance.now()
    for (const line of text.split(/\r?\n/)) {
      if (!line)
        continue
      logLines.push({ raw: line, json: parseJsonLine(line), rxAt: now })
    }
  }
  const handle = await startDevServer({
    dataDirPrefix: opts.dataDirPrefix,
    env: opts.env,
    onStdio: handleChunk,
  })
  return { ...handle, logLines }
}

// ──────────────────────────────────────────────
// Browser-side RPC mark wiring
// ──────────────────────────────────────────────

/**
 * Install leapmux:rpc-send / leapmux:rpc-recv listeners and initialize
 * `window.__rpcMarks` as an empty array. The listeners are only added
 * once per page — calling this helper again resets the array but does
 * not re-register, so iteration loops can zero the buffer between
 * samples without piling up duplicate listeners.
 *
 * Callers read `window.__rpcMarks` directly (keeps the read pattern
 * obvious at the call site; there's no single shared "snapshot" shape
 * since each spec combines marks with its own DOM state).
 */
export async function installRpcListeners(page: Page): Promise<void> {
  await page.evaluate(() => {
    interface RpcWindow {
      __rpcMarks?: Array<{ type: 'rpc-send' | 'rpc-recv', method: string, at: number, ok?: boolean }>
      __rpcListenersInstalled?: boolean
    }
    const w = window as unknown as RpcWindow
    w.__rpcMarks = []
    if (w.__rpcListenersInstalled)
      return
    w.__rpcListenersInstalled = true
    window.addEventListener('leapmux:rpc-send', (ev) => {
      const d = (ev as CustomEvent<{ method: string, at: number }>).detail
      w.__rpcMarks!.push({ type: 'rpc-send', method: d.method, at: d.at })
    })
    window.addEventListener('leapmux:rpc-recv', (ev) => {
      const d = (ev as CustomEvent<{ method: string, at: number, ok: boolean }>).detail
      w.__rpcMarks!.push({ type: 'rpc-recv', method: d.method, at: d.at, ok: d.ok })
    })
  })
}

// ──────────────────────────────────────────────
// Worker phase extraction
// ──────────────────────────────────────────────

export interface ClockAnchor {
  /** performance.now() captured in the browser at click time. */
  perf: number
  /** Date.now() captured alongside `perf`, used to translate slog's wall timestamps. */
  wall: number
}

export interface ExtractWorkerMarksOptions {
  /** slog marker to filter on, e.g. 'agent_startup_timing'. */
  marker: string
  /** Log row field holding the entity id, e.g. 'agent_id' or 'tab_id'. */
  idField: string
  /** Only include rows whose `idField` equals this value. */
  idValue: string
  /** Formatter for the returned mark name; receives the raw JSON row. */
  name: (row: Record<string, unknown>) => string
}

/**
 * Scan stderr log lines for backend phase markers and convert their
 * wall-clock timestamps into the browser's performance.now clock via
 * the given anchor. Shared by perf specs so they need only specify
 * their marker + id field.
 */
export function extractWorkerMarks(
  logLines: LogLine[],
  logOffset: number,
  anchor: ClockAnchor,
  opts: ExtractWorkerMarksOptions,
): PhaseMark[] {
  const out: PhaseMark[] = []
  for (let i = logOffset; i < logLines.length; i++) {
    const j = logLines[i]?.json
    if (!j || j.marker !== opts.marker || j[opts.idField] !== opts.idValue)
      continue
    const timeStr = String(j.time ?? '')
    if (!timeStr)
      continue
    const wallMs = new Date(timeStr).getTime()
    out.push({
      name: opts.name(j),
      tMs: anchor.perf + (wallMs - anchor.wall),
    })
  }
  return out
}

// ──────────────────────────────────────────────
// Timeline formatter
// ──────────────────────────────────────────────

/**
 * Render a sorted mark list as an ASCII table with absolute and delta
 * milliseconds per phase. Input must already be sorted by tMs.
 */
export function renderTimeline(marks: PhaseMark[]): string {
  if (marks.length === 0)
    return '(no marks captured)'
  const t0 = marks[0]!.tMs
  const nameWidth = Math.max(...marks.map(m => m.name.length))
  const lines: string[] = []
  lines.push(`${'phase'.padEnd(nameWidth)}  abs (ms)     Δ (ms)`)
  lines.push(`${'-'.repeat(nameWidth)}  --------   --------`)
  for (let i = 0; i < marks.length; i++) {
    const m = marks[i]!
    const abs = m.tMs - t0
    const delta = i === 0 ? 0 : m.tMs - marks[i - 1]!.tMs
    lines.push(`${m.name.padEnd(nameWidth)}  ${abs.toFixed(1).padStart(8)}   ${delta.toFixed(1).padStart(8)}`)
  }
  return lines.join('\n')
}
