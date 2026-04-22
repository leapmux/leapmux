/* eslint-disable no-console */
/**
 * Measures the end-to-end latency of opening a new Claude Code agent tab
 * and produces a phase-by-phase timeline breakdown.
 *
 * Backend stderr capture + JSON parse and the ASCII timeline renderer
 * live in ./helpers/timingFixture.ts; this file supplies the spec-
 * specific env (LEAPMUX_TRACE_AGENT_STARTUP) and the per-iteration DOM
 * observation logic.
 */
import type { LogLine, PhaseMark, TimingServer } from './helpers/timingFixture'

import { expect, test } from '@playwright/test'
import {
  createWorkspaceViaAPI,
  deleteWorkspaceViaAPI,
  openAgentViaAPI,
} from './helpers/api'
import { stopDevServer } from './helpers/devServer'
import { extractWorkerMarks, installRpcListeners, renderTimeline, startTimingServer } from './helpers/timingFixture'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

/**
 * Return the agent_id of the first handler_begin marker logged after
 * the given log offset. Used by the timing test to identify the agent
 * opened by a specific click in a multi-iteration loop.
 */
function findNewAgentId(logLines: LogLine[], offset: number): string | null {
  for (let i = offset; i < logLines.length; i++) {
    const j = logLines[i]?.json
    if (j && j.marker === 'agent_startup_timing' && j.phase === 'handler_begin' && typeof j.agent_id === 'string')
      return j.agent_id
  }
  return null
}

// ──────────────────────────────────────────────
// The test
// ──────────────────────────────────────────────

test.describe('Claude Code agent open timing', () => {
  // This test reports timing; it should not retry.
  test.describe.configure({ retries: 0 })

  let srv: TimingServer

  test.beforeAll(async () => {
    srv = await startTimingServer({
      dataDirPrefix: 'leapmux-timing-e2e',
      env: {
        LEAPMUX_CLAUDE_DEFAULT_MODEL: 'sonnet',
        LEAPMUX_CLAUDE_DEFAULT_EFFORT: 'low',
        LEAPMUX_WORKER_NAME: 'Local',
        LEAPMUX_TRACE_AGENT_STARTUP: '1',
      },
    })
  })

  test.afterAll(async () => {
    if (srv)
      await stopDevServer(srv)
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

    await installRpcListeners(page)

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

      const backendMarks = extractWorkerMarks(
        srv.logLines,
        logsBefore,
        { perf: tClickMs, wall: clockAnchorWallMs },
        {
          marker: 'agent_startup_timing',
          idField: 'agent_id',
          idValue: iterAgentId,
          name: row => `worker:${String(row.phase)}`,
        },
      )

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

      // Soft assertion: the tab DOM must appear within 1s of the click.
      // OpenAgent's sync prologue now performs only validation + a DB
      // insert — all expensive work (worktree creation, git status,
      // subprocess launch) happens in runAgentStartup afterwards — so
      // the perceived-latency budget is well under the pre-split ~5s.
      // Iteration 0 includes one-time SolidJS warm-up so the budget is
      // generous; the median across iterations is typically well under
      // 100ms in the printed table.
      expect(tTabDomMs - tClickMs).toBeLessThan(1000)

      // Sanity: every expected backend phase present on iter 0.
      if (iter === 0) {
        const seen = new Set(backendMarks.map(m => m.name.replace(/^worker:/, '')))
        for (const expected of [
          'handler_begin',
          'gitmode_validated',
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
