/* eslint-disable no-console */
/**
 * Measures the end-to-end latency of opening a new Claude Code agent tab
 * and produces a phase-by-phase timeline breakdown.
 *
 * Why its own fixture: we need
 *   (a) `LEAPMUX_TRACE_AGENT_STARTUP=1` so the worker emits phase markers
 *   (b) the backend stderr captured (the shared fixture drains it)
 * so we reimplement the small bits of fixtures.ts that we need.
 */
import type { Buffer } from 'node:buffer'
import type { ChildProcess } from 'node:child_process'
import { spawn } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import process from 'node:process'
import { expect, test } from '@playwright/test'
import {
  createWorkspaceViaAPI,
  deleteWorkspaceViaAPI,
  getAdminOrgId,
  getWorkerId,
  loginViaAPI,
  openAgentViaAPI,
} from './helpers/api'
import { findFreePort, getGlobalState, waitForServer } from './helpers/server'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

// ──────────────────────────────────────────────
// Phase enum & helpers
// ──────────────────────────────────────────────

interface PhaseMark { name: string, tMs: number }

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

/**
 * Return the agent_id of the first handler_begin marker logged after
 * the given log offset. Used by the timing test to identify the agent
 * opened by a specific click in a multi-iteration loop.
 */
function findNewAgentId(logLines: Array<{ json: Record<string, unknown> | null }>, offset: number): string | null {
  for (let i = offset; i < logLines.length; i++) {
    const j = logLines[i]?.json
    if (j && j.marker === 'agent_startup_timing' && j.phase === 'handler_begin' && typeof j.agent_id === 'string')
      return j.agent_id
  }
  return null
}

// ──────────────────────────────────────────────
// Local fixture: dev server with stderr capture + timing env var
// ──────────────────────────────────────────────

interface TimingServer {
  hubUrl: string
  adminToken: string
  adminOrgId: string
  workerId: string
  proc: ChildProcess
  dataDir: string
  /** All backend log lines emitted since server start, parsed if JSON. */
  logLines: Array<{ raw: string, json: Record<string, unknown> | null, rxAt: number }>
}

async function startTimingServer(): Promise<TimingServer> {
  const { binaryPath } = getGlobalState()
  const dataDir = mkdtempSync(join(tmpdir(), 'leapmux-timing-e2e-'))
  const port = await findFreePort()
  const hubUrl = `http://localhost:${port}`

  const proc = spawn(binaryPath, ['dev', '-addr', `:${port}`, '-data-dir', dataDir], {
    stdio: ['ignore', 'pipe', 'pipe'],
    env: {
      ...process.env,
      LEAPMUX_CLAUDE_DEFAULT_MODEL: 'sonnet',
      LEAPMUX_CLAUDE_DEFAULT_EFFORT: 'low',
      LEAPMUX_WORKER_NAME: 'Local',
      LEAPMUX_TRACE_AGENT_STARTUP: '1',
    },
  })

  const logLines: TimingServer['logLines'] = []
  const handleChunk = (chunk: Buffer) => {
    const text = chunk.toString('utf8')
    const now = performance.now()
    for (const line of text.split(/\r?\n/)) {
      if (!line)
        continue
      logLines.push({ raw: line, json: parseJsonLine(line), rxAt: now })
    }
  }
  proc.stdout?.on('data', handleChunk)
  proc.stderr?.on('data', handleChunk)

  await waitForServer(hubUrl)
  const adminToken = await loginViaAPI(hubUrl, 'admin', 'admin123')
  const adminOrgId = await getAdminOrgId(hubUrl, adminToken)
  const workerId = await getWorkerId(hubUrl, adminToken)

  return { hubUrl, adminToken, adminOrgId, workerId, proc, dataDir, logLines }
}

async function stopTimingServer(srv: TimingServer): Promise<void> {
  srv.proc.kill('SIGTERM')
  await new Promise(r => setTimeout(r, 1000))
  try {
    srv.proc.kill('SIGKILL')
  }
  catch { /* already dead */ }
  rmSync(srv.dataDir, { recursive: true, force: true })
}

// ──────────────────────────────────────────────
// Timeline formatter
// ──────────────────────────────────────────────

function renderTimeline(marks: PhaseMark[]): string {
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

// ──────────────────────────────────────────────
// The test
// ──────────────────────────────────────────────

test.describe('Claude Code agent open timing', () => {
  // This test reports timing; it should not retry.
  test.describe.configure({ retries: 0 })

  let srv: TimingServer

  test.beforeAll(async () => {
    srv = await startTimingServer()
  })

  test.afterAll(async () => {
    if (srv)
      await stopTimingServer(srv)
  })

  const ITERATIONS = 3

  test('produces a phase-by-phase breakdown', async ({ browser }, testInfo) => {
    const context = await browser.newContext({ baseURL: srv.hubUrl })
    const page = await context.newPage()

    // Workspace with one pre-existing agent via API, so the worker has
    // already done one Claude startup. We then measure ITERATIONS more
    // UI-initiated opens. The first measured iteration still incurs full
    // subprocess spawn + handshake (each Claude Code instance is a fresh
    // subprocess); what the pre-existing agent eliminates is first-time
    // per-process effects like filesystem cache warm-up for `claude`.
    const workspaceId = await createWorkspaceViaAPI(
      srv.hubUrl,
      srv.adminToken,
      `timing-${Date.now()}`,
      srv.adminOrgId,
    )
    await openAgentViaAPI(srv.hubUrl, srv.adminToken, srv.workerId, workspaceId)
    await loginViaToken(page, srv.adminToken)
    await page.goto(`/o/admin/workspace/${workspaceId}`)
    await waitForWorkspaceReady(page)
    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)
    await expect(page.locator('[data-testid="chat-editor"] .ProseMirror')).toBeVisible()

    await page.evaluate(() => {
      interface RpcMark { type: 'rpc-send' | 'rpc-recv', method: string, at: number, ok?: boolean }
      const w = window as unknown as { __rpcMarks?: RpcMark[] }
      w.__rpcMarks = []
      window.addEventListener('leapmux:rpc-send', (ev) => {
        const d = (ev as CustomEvent<{ method: string, at: number }>).detail
        w.__rpcMarks!.push({ type: 'rpc-send', method: d.method, at: d.at })
      })
      window.addEventListener('leapmux:rpc-recv', (ev) => {
        const d = (ev as CustomEvent<{ method: string, at: number, ok: boolean }>).detail
        w.__rpcMarks!.push({ type: 'rpc-recv', method: d.method, at: d.at, ok: d.ok })
      })
    })

    const runs: PhaseMark[][] = []

    for (let iter = 0; iter < ITERATIONS; iter++) {
      // Reset per-iteration state and install a MutationObserver that
      // records DOM-level timestamps the instant the new tab element (and
      // later, the new editor) appears. This avoids Playwright's ~250ms
      // default poll interval from dominating short-lived UI timings.
      await page.evaluate(() => {
        const w = window as unknown as {
          __rpcMarks?: Array<unknown>
          __tabAppearedAt?: number | null
          __editorAppearedAt?: number | null
          __startupOverlayGoneAt?: number | null
          __tabObserver?: MutationObserver
          __tabBaseline?: number
        }
        w.__rpcMarks = []
        w.__tabAppearedAt = null
        w.__editorAppearedAt = null
        w.__startupOverlayGoneAt = null
        w.__tabBaseline = document.querySelectorAll('[data-testid="tab"][data-tab-type="agent"]').length
        const priorEditor = document.querySelector('[data-testid="chat-editor"] .ProseMirror')
        w.__tabObserver?.disconnect()
        w.__tabObserver = new MutationObserver(() => {
          if (w.__tabAppearedAt == null) {
            const tabs = document.querySelectorAll('[data-testid="tab"][data-tab-type="agent"]')
            if (tabs.length > (w.__tabBaseline ?? 0))
              w.__tabAppearedAt = performance.now()
          }
          if (w.__editorAppearedAt == null) {
            const ed = document.querySelector('[data-testid="chat-editor"] .ProseMirror')
            // A fresh .ProseMirror for the new tab (DOM node differs from
            // the pre-click instance).
            if (ed && ed !== priorEditor)
              w.__editorAppearedAt = performance.now()
          }
          // The "Starting Claude Code…" overlay is removed when the
          // agent transitions from STARTING → ACTIVE. Capture the DOM
          // moment that happens — this is the practical user-visible
          // signal that the agent is ready.
          if (w.__startupOverlayGoneAt == null && w.__tabAppearedAt != null) {
            const overlay = document.querySelector('[data-testid="agent-startup-overlay"]')
            if (!overlay)
              w.__startupOverlayGoneAt = performance.now()
          }
        })
        w.__tabObserver.observe(document.body, { childList: true, subtree: true })
      })
      const logsBefore = srv.logLines.length
      const tabsBefore = await page.locator('[data-testid="tab"][data-tab-type="agent"]').count()

      const clockAnchor = await page.evaluate(() => ({ perf: performance.now(), wall: Date.now() }))
      const tClickMs = clockAnchor.perf
      const clockAnchorWallMs = clockAnchor.wall
      await page.locator('[data-testid^="new-agent-button"]').first().click()

      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(tabsBefore + 1)
      await expect(page.locator('[data-testid="chat-editor"] .ProseMirror')).toBeVisible()
      // Since OpenAgent now returns immediately (status=STARTING), we
      // need to actively wait for this specific iteration's subprocess
      // startup to finish. Identify the new agent_id from the first
      // handler_begin marker after logsBefore, then wait for its
      // "agent started" log.
      await expect.poll(() => findNewAgentId(srv.logLines, logsBefore) !== null, { timeout: 10_000 }).toBeTruthy()
      const iterAgentId = findNewAgentId(srv.logLines, logsBefore)!
      await expect.poll(() => srv.logLines.slice(logsBefore).some(l => l.json?.msg === 'agent started' && l.json?.agent_id === iterAgentId), { timeout: 60_000 }).toBeTruthy()
      // The MutationObserver installed above gives us the instant the
      // new tab DOM node was attached and the instant a new .ProseMirror
      // mounted for the new tab; these avoid Playwright's poll-interval
      // granularity (~250ms) that distorts short-lived UI phases.
      // Force a final observation of the overlay-removed signal in
      // case the MutationObserver's last callback already ran before
      // ACTIVE arrived (i.e. without a subsequent mutation).
      await page.locator('[data-testid="agent-startup-overlay"]').waitFor({ state: 'detached', timeout: 60_000 })
      const { tTabDomMs, tEditorReadyMs, tStatusActiveMs } = await page.evaluate(() => {
        const w = window as unknown as {
          __tabAppearedAt?: number | null
          __editorAppearedAt?: number | null
          __startupOverlayGoneAt?: number | null
        }
        return {
          tTabDomMs: w.__tabAppearedAt ?? performance.now(),
          tEditorReadyMs: w.__editorAppearedAt ?? performance.now(),
          tStatusActiveMs: w.__startupOverlayGoneAt ?? performance.now(),
        }
      })

      const rpcMarks = await page.evaluate(() => {
        const w = window as unknown as { __rpcMarks?: Array<{ type: string, method: string, at: number }> }
        return w.__rpcMarks ?? []
      })
      const openSend = rpcMarks.find(m => m.type === 'rpc-send' && m.method === 'OpenAgent')
      const openRecv = rpcMarks.find(m => m.type === 'rpc-recv' && m.method === 'OpenAgent')

      const newLogs = srv.logLines.slice(logsBefore)
      const timingRows = newLogs
        .map(l => l.json)
        .filter((j): j is Record<string, unknown> => !!j && j.marker === 'agent_startup_timing')
      const backendPhases = timingRows
        .filter(r => r.agent_id === iterAgentId)
        .map(r => ({ phase: String(r.phase), wallMs: new Date(String(r.time ?? '')).getTime() }))
      const backendMarks: PhaseMark[] = backendPhases.map(p => ({
        name: `worker:${p.phase}`,
        tMs: tClickMs + (p.wallMs - clockAnchorWallMs),
      }))

      const marks: PhaseMark[] = []
      marks.push({ name: 'ui:click', tMs: tClickMs })
      if (openSend)
        marks.push({ name: 'ui:rpc-send', tMs: openSend.at })
      marks.push(...backendMarks)
      if (openRecv)
        marks.push({ name: 'ui:rpc-recv', tMs: openRecv.at })
      marks.push({ name: 'ui:tab-dom-visible', tMs: tTabDomMs })
      marks.push({ name: 'ui:editor-ready', tMs: tEditorReadyMs })
      // The "agent-status-active" mark is when the in-tab Starting…
      // overlay is removed by the STARTING → ACTIVE status broadcast.
      // It's the user-visible signal that the agent is ready to chat.
      marks.push({ name: 'ui:agent-status-active', tMs: tStatusActiveMs })
      marks.sort((a, b) => a.tMs - b.tMs)
      runs.push(marks)

      // Soft assertion: the tab DOM must appear well under 2s of the
      // click — this is the perceived-latency improvement the OpenAgent
      // split is meant to deliver. (Pre-split the same path took ~5s.)
      // Iteration 0 includes one-time SolidJS warm-up so the budget is
      // generous; the median across iterations is typically well under
      // 100ms in the printed table.
      expect(tTabDomMs - tClickMs).toBeLessThan(2000)

      // Sanity: every expected backend phase present on iter 0.
      if (iter === 0) {
        const seen = new Set(backendPhases.map(p => p.phase))
        for (const expected of [
          'handler_begin',
          'gitmode_applied',
          'before_start_agent',
          'claude_begin',
          'before_exec_start',
          'after_exec_start',
          'before_initialize',
          'control_stdin_write',
          'preamble_delimiter_seen',
          'first_agent_line',
          'after_initialize',
          'before_permission_mode',
          'after_permission_mode',
          'after_start_agent',
          'before_response',
          'response_sent',
        ]) {
          expect(seen, `missing backend phase ${expected}`).toContain(expected)
        }
      }
    }

    // Render per-iteration tables + a summary (median Δ per phase).
    const parts: string[] = []
    for (let i = 0; i < runs.length; i++) {
      parts.push(`=== iteration ${i + 1} ===`)
      parts.push(renderTimeline(runs[i]!))
      parts.push('')
    }
    parts.push('=== median Δ (ms) across iterations ===')
    parts.push(renderMedianDeltas(runs))

    const report = parts.join('\n')
    console.log(`\n──── Claude Code agent open timing ────\n${report}\n───────────────────────────────────────\n`)
    await testInfo.attach('agent-open-timeline', { body: report, contentType: 'text/plain' })

    await deleteWorkspaceViaAPI(srv.hubUrl, srv.adminToken, workspaceId).catch(() => {})
    await context.close()
  })
})

function renderMedianDeltas(runs: PhaseMark[][]): string {
  if (runs.length === 0)
    return '(no runs)'
  // Use the first run's phase order as the canonical order.
  const canonical = runs[0]!.map(m => m.name)
  const nameWidth = Math.max(...canonical.map(n => n.length))
  const lines: string[] = []
  lines.push(`${'phase'.padEnd(nameWidth)}  median Δ (ms)`)
  lines.push(`${'-'.repeat(nameWidth)}  -------------`)
  for (let i = 0; i < canonical.length; i++) {
    const name = canonical[i]!
    const deltas: number[] = []
    for (const r of runs) {
      const idx = r.findIndex(m => m.name === name)
      if (idx <= 0)
        continue
      deltas.push(r[idx]!.tMs - r[idx - 1]!.tMs)
    }
    if (deltas.length === 0)
      continue
    deltas.sort((a, b) => a - b)
    const median = deltas[Math.floor(deltas.length / 2)]!
    lines.push(`${name.padEnd(nameWidth)}  ${median.toFixed(1).padStart(10)}`)
  }
  return lines.join('\n')
}
