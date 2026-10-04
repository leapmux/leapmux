import type { LogLine, PhaseMark, TimingWorker } from '../helpers/timingFixture'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { test as fixturesTest } from '../fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from '../helpers/api'
import { findBinary } from '../helpers/binaryOnPath'
import { withCleanup } from '../helpers/cleanup'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { extractWorkerMarks, installRpcListeners, renderTimeline, withTimingWorker } from '../helpers/timingFixture'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, expectSettingsChip, loginViaToken, openWorkspace, settingsBar } from '../helpers/ui'

/** Measure native startup on a traced private Worker against the suite Hub. */
const timingTest = fixturesTest.extend<{ timingWorker: TimingWorker }>({
  timingWorker: async ({ leapmuxServer }, use) => withTimingWorker(leapmuxServer, {
    dataDirPrefix: 'leapmux-timing-e2e',
    env: { LEAPMUX_TRACE_AGENT_STARTUP: '1' },
  }, use),
})

const startupErrorTest = fixturesTest.extend<{ failingWorker: TimingWorker }>({
  failingWorker: async ({ leapmuxServer }, use) => withTimingWorker(leapmuxServer, {
    dataDirPrefix: 'leapmux-startup-err',
    env: { SHELL: '/usr/bin/false', LEAPMUX_WORKER_AGENT_STARTUP_TIMEOUT_SECONDS: '5' },
  }, use),
})

fixturesTest.describe('Agent Settings', () => {
  fixturesTest('default settings on startup', async ({ authenticatedWorkspace, page }) => {
    void authenticatedWorkspace
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()
    await expectSettingsChip(page, 'Sonnet')
    await expectSettingsChip(page, 'Default')
  })
})

function findNewAgentId(logLines: LogLine[], offset: number): string | null {
  for (let i = offset; i < logLines.length; i++) {
    const j = logLines[i]?.json
    if (j && j.marker === 'agent_startup_timing' && j.phase === 'handler_begin' && typeof j.agent_id === 'string')
      return j.agent_id
  }
  return null
}

timingTest.describe('Claude Code agent open timing', () => {
  timingTest.describe.configure({ retries: 0 })
  const ITERATIONS = 3

  timingTest('produces a phase-by-phase breakdown', async ({ page, timingWorker }, testInfo) => {
    const srv = { ...timingWorker.server, logLines: timingWorker.logLines }

    // Start one Claude agent before the measured opens to populate the Worker's filesystem cache.
    // Each measured open still starts a new native process and performs its handshake.
    const workspaceId = await createWorkspaceViaAPI(
      srv.hubUrl,
      srv.adminToken,
      `timing-${Date.now()}`,
    )
    await withCleanup(async () => {
      await openAgentViaAPI(srv.hubUrl, srv.adminToken, srv.workerId, workspaceId)
      await loginViaToken(page, srv.adminToken)
      await openWorkspace(page, workspaceId)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)
      await expect(page.locator('[data-testid="composer-editor"] .ProseMirror')).toBeVisible()

      await installRpcListeners(page)

      const runs: PhaseMark[][] = []
      /** Measure the time from the click to tab creation. Assert the median below. */
      const clickToTabMs: number[] = []

      for (let iter = 0; iter < ITERATIONS; iter++) {
        // Reset the marks before each iteration. Observe tab and editor creation without the delay of Playwright polling.
        await page.evaluate(() => {
          const w: Window & {
            __rpcMarks?: Array<unknown>
            __tabAppearedAt?: number | null
            __editorAppearedAt?: number | null
            __startupOverlayGoneAt?: number | null
            __tabObserver?: MutationObserver
            __tabBaseline?: number
          } = window
          w.__rpcMarks = []
          w.__tabAppearedAt = null
          w.__editorAppearedAt = null
          w.__startupOverlayGoneAt = null
          w.__tabBaseline = document.querySelectorAll('[data-testid="tab"][data-tab-type="agent"]').length
          const priorEditor = document.querySelector('[data-testid="composer-editor"] .ProseMirror')
          w.__tabObserver?.disconnect()
          w.__tabObserver = new MutationObserver(() => {
            if (w.__tabAppearedAt == null) {
              const tabs = document.querySelectorAll('[data-testid="tab"][data-tab-type="agent"]')
              if (tabs.length > (w.__tabBaseline ?? 0))
                w.__tabAppearedAt = performance.now()
            }
            if (w.__editorAppearedAt == null) {
              const ed = document.querySelector('[data-testid="composer-editor"] .ProseMirror')
              // Require the new tab's editor node to differ from the editor before the click.
              if (ed && ed !== priorEditor)
                w.__editorAppearedAt = performance.now()
            }
            // ACTIVE removes the startup overlay. Record that visible readiness change.
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
        await page.getByTestId(`new-agent-button-${AgentProvider.CLAUDE_CODE}`).filter({ visible: true }).first().click()

        await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(tabsBefore + 1)
        await expect(page.locator('[data-testid="composer-editor"] .ProseMirror')).toBeVisible()
        // OpenAgent returns STARTING before native startup finishes. Match this iteration's agent ID and wait for its actual startup log.
        await expect.poll(() => findNewAgentId(srv.logLines, logsBefore) !== null).toBeTruthy()
        const iterAgentId = findNewAgentId(srv.logLines, logsBefore)!
        await expect.poll(() => srv.logLines.slice(logsBefore).some(l => l.json?.msg === 'agent started' && l.json?.agent_id === iterAgentId)).toBeTruthy()
        // The observer records the tab and editor timestamps directly.
        // Also require removal of the startup overlay before reading the completed marks.
        await page.locator('[data-testid="agent-startup-overlay"]').waitFor({ state: 'detached' })
        const { tTabDomMs, tEditorReadyMs, tStatusActiveMs } = await page.evaluate(() => {
          const w: Window & {
            __tabAppearedAt?: number | null
            __editorAppearedAt?: number | null
            __startupOverlayGoneAt?: number | null
          } = window
          return {
            tTabDomMs: w.__tabAppearedAt ?? performance.now(),
            tEditorReadyMs: w.__editorAppearedAt ?? performance.now(),
            tStatusActiveMs: w.__startupOverlayGoneAt ?? performance.now(),
          }
        })

        const rpcMarks = await page.evaluate(() => {
          const w: Window & { __rpcMarks?: Array<{ type: string, method: string, at: number }> } = window
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
        // ACTIVE removes the startup overlay and tells the user that the agent accepts input.
        marks.push({ name: 'ui:agent-status-active', tMs: tStatusActiveMs })
        marks.sort((a, b) => a.tMs - b.tMs)
        runs.push(marks)

        clickToTabMs.push(tTabDomMs - tClickMs)

        // Require every backend phase in the first iteration.
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

      // Require the median tab creation time to stay below one second.
      // OpenAgent validates and inserts the row before asynchronous native startup.
      // A delayed scheduler can affect one sample, so use the median of all three iterations.
      const sortedClickToTab = [...clickToTabMs].sort((a, b) => a - b)
      const medianClickToTab = sortedClickToTab[Math.floor(sortedClickToTab.length / 2)]!
      expect(medianClickToTab, `click→tab-DOM samples: ${clickToTabMs.map(v => v.toFixed(0)).join(', ')}ms`)
        .toBeLessThan(1000)

      // Render each iteration and the median time difference for each phase.
      const parts: string[] = []
      for (let i = 0; i < runs.length; i++) {
        parts.push(`=== iteration ${i + 1} ===`)
        parts.push(renderTimeline(runs[i]!))
        parts.push('')
      }
      parts.push('=== median Δ (ms) across iterations ===')
      parts.push(renderMedianDeltas(runs))

      const report = parts.join('\n')
      process.stdout.write(`\n──── Claude Code agent open timing ────\n${report}\n───────────────────────────────────────\n`)
      await testInfo.attach('agent-open-timeline', { body: report, contentType: 'text/plain' })
    }, () => deleteWorkspaceViaAPI(srv.hubUrl, srv.adminToken, workspaceId))
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

fixturesTest.describe('Claude Code agent startup queue', () => {
  fixturesTest('queues a typed-during-startup message and delivers it on ACTIVE', async ({ page, authenticatedWorkspace, leapmuxServer, modelScript }) => {
    const executable = findBinary('claude', leapmuxServer.agentEnv)
    if (!executable)
      throw new Error('The isolated Claude executable is absent.')
    await exerciseAgentStartup({ page, modelScript, leapmuxServer, provider: AgentProvider.CLAUDE_CODE, workspaceId: authenticatedWorkspace.workspaceId }, {
      launch: { binaryName: 'claude', executable, holdWhen: ['--input-format', 'stream-json'] },
      prompt: ARITHMETIC_PROMPT,
      answer: ARITHMETIC_ANSWER_TEXT,
    })
    await expectAssistantAnswer(page)
  })
})

startupErrorTest.describe('Claude Code agent startup error', () => {
  startupErrorTest('shows in-tab error and rejects subsequent sends', async ({ page, failingWorker }) => {
    const srv = failingWorker.server

    const workspaceId = await createWorkspaceViaAPI(
      srv.hubUrl,
      srv.adminToken,
      `startup-err-${Date.now()}`,
    )
    await withCleanup(async () => {
      await openAgentViaAPI(srv.hubUrl, srv.adminToken, srv.workerId, workspaceId)
      await loginViaToken(page, srv.adminToken)
      await openWorkspace(page, workspaceId)

      // The startup-error panel must appear with the formatted error.
      const errorPanel = page.locator('[data-testid="agent-startup-error"]')
      await expect(errorPanel).toBeVisible()
      await expect(errorPanel.locator('h2')).toContainText('failed to start')
      await expect(errorPanel.locator('pre code')).toBeVisible()

      // The Worker retains the input as a failed queue item.
      const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
      await editor.click()
      await page.keyboard.type('hello')
      await page.keyboard.press('Meta+Enter')
      await expect(page.getByTestId('agent-input-queue')).toContainText('Failed')
      await expect(page.getByTestId('agent-input-queue')).toContainText('hello')
    }, () => deleteWorkspaceViaAPI(srv.hubUrl, srv.adminToken, workspaceId))
  })
})
