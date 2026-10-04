/** Capture backend timing lines and browser RPC marks, then render their timelines. */
import type { Page } from '@playwright/test'
import type { Buffer } from 'node:buffer'
import type { ServerInfo } from '../fixtures'
import { isObject } from '../../../src/lib/jsonPick'
import { withNativeWorker } from './nativeWorker'
import { createProcessOutputLineDecoder } from './processOutputLines'

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

// ──────────────────────────────────────────────
// Process stream capture
// ──────────────────────────────────────────────

/** Read one JSON object from a complete log line. Return null for other output. */
function parseJsonLine(line: string): Record<string, unknown> | null {
  const trimmed = line.trim()
  if (!trimmed.startsWith('{'))
    return null
  try {
    const value: unknown = JSON.parse(trimmed)
    return isObject(value) ? value : null
  }
  catch {
    return null
  }
}

/** Keep independent UTF-8 and line state for stdout and stderr. */
export function createTimingLogReceiver(clock: () => number = () => performance.now()): {
  logLines: LogLine[]
  receive: (chunk: Buffer, stream?: 'stdout' | 'stderr') => void
  end: (stream?: 'stdout' | 'stderr') => void
} {
  const logLines: LogLine[] = []
  const streamReceiver = () => {
    let receivedAt = 0
    const decoder = createProcessOutputLineDecoder((line) => {
      if (line)
        logLines.push({ raw: line, json: parseJsonLine(line), rxAt: receivedAt })
    })
    return {
      receive: (chunk: Buffer) => {
        receivedAt = clock()
        decoder.write(chunk)
      },
      end: decoder.end,
    }
  }
  const streams = { stdout: streamReceiver(), stderr: streamReceiver() }
  return {
    logLines,
    receive: (chunk, stream = 'stderr') => streams[stream].receive(chunk),
    end: (stream) => {
      if (stream) {
        streams[stream].end()
      }
      else {
        streams.stdout.end()
        streams.stderr.end()
      }
    },
  }
}

export interface TimingWorker {
  server: ServerInfo
  logLines: LogLine[]
  dataDir: string
}

/** Capture one traced private Worker without starting another Hub. */
export async function withTimingWorker(
  server: ServerInfo,
  options: { dataDirPrefix: string, env?: NodeJS.ProcessEnv },
  use: (worker: TimingWorker) => Promise<void>,
): Promise<void> {
  const receiver = createTimingLogReceiver()
  await withNativeWorker(server, {
    dataDirPrefix: options.dataDirPrefix,
    workerName: 'Timing Worker',
    ...(options.env ? { env: options.env } : {}),
    onStdio: receiver.receive,
    onStdioEnd: receiver.end,
  }, worker => use({ server: worker.server, logLines: receiver.logLines, dataDir: worker.dataDir }))
}

// ──────────────────────────────────────────────
// Browser-side RPC mark wiring
// ──────────────────────────────────────────────

/**
 * Install the leapmux:rpc-send and leapmux:rpc-recv listeners once in each document.
 * Reset window.__rpcMarks on each call. Iterations then reuse the listeners without duplicate entries.
 * Callers read the marks directly and combine them with their own DOM state.
 */
export async function installRpcListeners(page: Page): Promise<void> {
  await page.evaluate(() => {
    interface RpcWindow {
      __rpcMarks?: Array<{ type: 'rpc-send' | 'rpc-recv', method: string, at: number, ok?: boolean }>
      __rpcListenersInstalled?: boolean
    }
    const w: Window & RpcWindow = window
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
 * Read backend phase markers from the captured process streams.
 * Convert each wall timestamp to the browser performance.now clock through the supplied anchor.
 * Each timing test supplies its marker and ID field.
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
 * Render absolute and delta milliseconds for each phase in an ASCII table. Sort the input by tMs before this call.
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
