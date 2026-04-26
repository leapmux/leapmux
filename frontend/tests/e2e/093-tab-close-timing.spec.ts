/* eslint-disable no-console */
/**
 * Measures the end-to-end latency of closing an agent tab and produces a
 * phase-by-phase timeline breakdown. Complements
 * `121-claude-agent-open-timing.spec.ts`.
 *
 * Instrumentation:
 *   - Browser: `leapmux:rpc-send` / `leapmux:rpc-recv` CustomEvents
 *     from `src/api/workerRpc.ts` (gated on `LEAPMUX_DEV`, enabled by
 *     the e2e runner) plus a MutationObserver for tab / dialog
 *     timestamps.
 *   - Worker: `LEAPMUX_TRACE_TAB_CLOSE=1` makes
 *     `backend/internal/worker/service/tabclosetrace.go` emit
 *     `marker=tab_close_timing` slog lines for the inspect RPC's inner
 *     phases (git_ctx_done, diff_and_push_done,
 *     worktree_count_done / branch_count_done, handler_end). We combine
 *     them with browser marks using a wall-clock anchor captured
 *     immediately before the click.
 *
 * Three scenarios:
 *   1. Two worktree tabs on the same worktree, close one. No prompt
 *      because `CountWorktreeTabs > 1`. This is the topology the user
 *      reported as "~1 second slow".
 *   2. Worktree last tab, dialog → Close anyway (KEEP).
 *   3. Worktree last tab, dialog → Delete (REMOVE).
 *
 * Point at a real repository by setting
 * `LEAPMUX_CLOSE_TIMING_REPO_DIR=/path/to/your/repo`. Without it, each
 * scenario creates a tiny synthetic repo under the test's dataDir.
 */
import type { Page, TestInfo } from '@playwright/test'
import type { ClockAnchor, LogLine, PhaseMark, RpcMark, TimingServer } from './helpers/timingFixture'
import { existsSync, realpathSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect, test } from '@playwright/test'
import { deleteWorkspaceViaAPI } from './helpers/api'
import { stopDevServer } from './helpers/devServer'
import { extractWorkerMarks, installRpcListeners, renderTimeline, startTimingServer } from './helpers/timingFixture'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'
import {
  createGitRepo,
  createWorkspaceWithWorktreeViaAPI,
  waitForPathDeleted,
} from './helpers/worktree'

// ─── Browser instrumentation ──────────────────────────────────────────

interface TimingWindow {
  __rpcMarks?: RpcMark[]
  __tabRemovedAt?: number | null
  __dialogVisibleAt?: number | null
  __dialogRemovedAt?: number | null
  __observer?: MutationObserver
  __tabBaseline?: number
}

async function installObservers(page: Page): Promise<void> {
  await installRpcListeners(page)
  await page.evaluate(() => {
    const w = window as unknown as TimingWindow
    w.__tabRemovedAt = null
    w.__dialogVisibleAt = null
    w.__dialogRemovedAt = null
    w.__tabBaseline = document.querySelectorAll('[data-testid="tab"][data-tab-type="agent"]').length
    w.__observer?.disconnect()
    w.__observer = new MutationObserver(() => {
      if (w.__tabRemovedAt == null) {
        const tabs = document.querySelectorAll('[data-testid="tab"][data-tab-type="agent"]').length
        if (tabs < (w.__tabBaseline ?? 0))
          w.__tabRemovedAt = performance.now()
      }
      if (w.__dialogVisibleAt == null) {
        const dialog = document.querySelector('dialog[open]')
        if (dialog && dialog.textContent?.includes('Close Last Tab'))
          w.__dialogVisibleAt = performance.now()
      }
      if (w.__dialogVisibleAt != null && w.__dialogRemovedAt == null) {
        const dialog = document.querySelector('dialog[open]')
        if (!dialog || !dialog.textContent?.includes('Close Last Tab'))
          w.__dialogRemovedAt = performance.now()
      }
    })
    w.__observer.observe(document.body, { childList: true, subtree: true })
  })
}

interface RawMarks {
  rpcMarks: RpcMark[]
  tabRemovedAt: number | null
  dialogVisibleAt: number | null
  dialogRemovedAt: number | null
}

async function snapshotMarks(page: Page): Promise<RawMarks> {
  return page.evaluate(() => {
    const w = window as unknown as TimingWindow
    return {
      rpcMarks: w.__rpcMarks ?? [],
      tabRemovedAt: w.__tabRemovedAt ?? null,
      dialogVisibleAt: w.__dialogVisibleAt ?? null,
      dialogRemovedAt: w.__dialogRemovedAt ?? null,
    }
  })
}

// ─── Combine browser + worker marks ───────────────────────────────────

function extractCloseWorkerMarks(logLines: LogLine[], logOffset: number, tabID: string, anchor: ClockAnchor): PhaseMark[] {
  return extractWorkerMarks(logLines, logOffset, anchor, {
    marker: 'tab_close_timing',
    idField: 'tab_id',
    idValue: tabID,
    name: row => `worker:${String(row.op ?? 'inspect')}:${String(row.phase)}`,
  })
}

function buildTimeline(
  tClickMs: number,
  raw: RawMarks,
  workerMarks: PhaseMark[],
  extra: PhaseMark[] = [],
): PhaseMark[] {
  const marks: PhaseMark[] = [{ name: 'ui:click', tMs: tClickMs }]
  const inspectSend = raw.rpcMarks.find(m => m.type === 'rpc-send' && m.method === 'InspectLastTabClose')
  const inspectRecv = raw.rpcMarks.find(m => m.type === 'rpc-recv' && m.method === 'InspectLastTabClose')
  const closeSend = raw.rpcMarks.find(m => m.type === 'rpc-send' && (m.method === 'CloseAgent' || m.method === 'CloseTerminal'))
  const closeRecv = raw.rpcMarks.find(m => m.type === 'rpc-recv' && (m.method === 'CloseAgent' || m.method === 'CloseTerminal'))
  if (inspectSend)
    marks.push({ name: `ui:rpc-send ${inspectSend.method}`, tMs: inspectSend.at })
  marks.push(...workerMarks)
  if (inspectRecv)
    marks.push({ name: `ui:rpc-recv ${inspectRecv.method}`, tMs: inspectRecv.at })
  if (raw.dialogVisibleAt != null)
    marks.push({ name: 'ui:dialog-visible', tMs: raw.dialogVisibleAt })
  if (raw.dialogRemovedAt != null)
    marks.push({ name: 'ui:dialog-closed', tMs: raw.dialogRemovedAt })
  if (raw.tabRemovedAt != null)
    marks.push({ name: 'ui:tab-dom-removed', tMs: raw.tabRemovedAt })
  if (closeSend)
    marks.push({ name: `ui:rpc-send ${closeSend.method}`, tMs: closeSend.at })
  if (closeRecv)
    marks.push({ name: `ui:rpc-recv ${closeRecv.method}`, tMs: closeRecv.at })
  marks.push(...extra)
  marks.sort((a, b) => a.tMs - b.tMs)
  return marks
}

// Identify the agent_id of the just-closed tab from the backend logs so
// we can match the correct tab_close_timing entries. The inspect
// handler_begin line carries tab_id as the agent id.
function findClosedTabID(logLines: LogLine[], logOffset: number): string | null {
  for (let i = logOffset; i < logLines.length; i++) {
    const j = logLines[i]?.json
    if (j && j.marker === 'tab_close_timing' && j.phase === 'handler_begin' && typeof j.tab_id === 'string')
      return j.tab_id
  }
  return null
}

// Await the close RPC round-trip and the backend handler_begin marker,
// then assemble the browser+worker timeline, render it, log it, and
// attach it to the Playwright report. Returns the snapshot of browser
// marks so the caller can make scenario-specific assertions (e.g. on
// dialogVisibleAt / tabRemovedAt).
async function captureCloseTimeline(
  page: Page,
  srv: TimingServer,
  logsBefore: number,
  anchor: ClockAnchor,
  testInfo: TestInfo,
  scenarioLabel: string,
  attachName: string,
  extra: PhaseMark[] = [],
): Promise<RawMarks> {
  await expect.poll(async () => {
    const r = await snapshotMarks(page)
    return r.rpcMarks.some(m => m.type === 'rpc-recv' && (m.method === 'CloseAgent' || m.method === 'CloseTerminal'))
  }, { timeout: 10_000 }).toBeTruthy()
  await expect.poll(() => findClosedTabID(srv.logLines, logsBefore) !== null, { timeout: 10_000 }).toBeTruthy()
  const closedTabID = findClosedTabID(srv.logLines, logsBefore)!

  const raw = await snapshotMarks(page)
  const workerMarks = extractCloseWorkerMarks(srv.logLines, logsBefore, closedTabID, anchor)
  const marks = buildTimeline(anchor.perf, raw, workerMarks, extra)
  const report = renderTimeline(marks)
  const border = '─'.repeat(scenarioLabel.length + 6)
  console.log(`\n──── ${scenarioLabel} ────\n${report}\n${border}\n`)
  await testInfo.attach(attachName, { body: report, contentType: 'text/plain' })
  return raw
}

// ─── The repo the scenarios operate on ────────────────────────────────

interface RepoCtx {
  /** Absolute path of the repo root (HEAD branch is whatever git init gives). */
  repoDir: string
  /** Whether the repo is owned by the test (safe to clean up via worktree rm). */
  synthetic: boolean
}

function getRepoCtx(dataDir: string, scenarioName: string): RepoCtx {
  const override = process.env.LEAPMUX_CLOSE_TIMING_REPO_DIR
  if (override) {
    return { repoDir: override, synthetic: false }
  }
  return { repoDir: createGitRepo(dataDir, scenarioName), synthetic: true }
}

// ─── Tests ────────────────────────────────────────────────────────────

test.describe('Tab close timing', () => {
  test.describe.configure({ retries: 0 })

  let srv: TimingServer

  test.beforeAll(async () => {
    srv = await startTimingServer({
      dataDirPrefix: 'leapmux-close-timing-e2e',
      env: {
        LEAPMUX_CLAUDE_DEFAULT_MODEL: 'sonnet',
        LEAPMUX_CLAUDE_DEFAULT_EFFORT: 'low',
        LEAPMUX_WORKER_NAME: 'Local',
        LEAPMUX_TRACE_TAB_CLOSE: '1',
      },
    })
  })

  test.afterAll(async () => {
    if (srv)
      await stopDevServer(srv)
  })

  test('scenario 1 — two worktree tabs on the same worktree, close one', async ({ browser }, testInfo) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = srv
    const ctx = getRepoCtx(dataDir, 'close-timing-scn1')

    // createWorkspaceWithWorktreeViaAPI opens agent 1 with createWorktree=true
    // on branch "scn1-branch". After the page loads, clicking "+ New Agent"
    // opens a second agent. The workspace's currentDir is the worktree, so
    // the new agent reuses the same worktree — ensureTrackedWorktree finds
    // the existing row and registers tab 2 against the same worktree_id.
    // Closing either tab leaves tabCount=1 for the worktree, so inspect
    // returns shouldPrompt=false.
    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'close-timing-scn1',
      adminOrgId,
      ctx.repoDir,
      'scn1-branch',
    )

    const context = await browser.newContext({ baseURL: hubUrl })
    const page = await context.newPage()
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)
      await page.locator('[data-testid^="new-agent-button"]').first().click()
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(2)

      await installObservers(page)

      const anchor = await page.evaluate(() => ({ perf: performance.now(), wall: Date.now() }))
      const logsBefore = srv.logLines.length
      await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().locator('[data-testid="tab-close"]').dispatchEvent('click')

      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)
      const raw = await captureCloseTimeline(
        page,
        srv,
        logsBefore,
        anchor,
        testInfo,
        'scenario 1: two-worktree-tabs, close one',
        'close-timing-scenario-1',
      )

      expect(raw.dialogVisibleAt, 'no dialog expected when worktree has >1 tab').toBeNull()
      expect(raw.tabRemovedAt, 'tab DOM should have been removed').not.toBeNull()
      expect(raw.tabRemovedAt! - anchor.perf).toBeLessThan(3000)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
      await context.close()
    }
  })

  test('scenario 2 — worktree close with "Close anyway" (KEEP)', async ({ browser }, testInfo) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = srv
    const ctx = getRepoCtx(dataDir, 'close-timing-scn2')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'close-timing-scn2',
      adminOrgId,
      ctx.repoDir,
      'scn2-branch',
    )

    const context = await browser.newContext({ baseURL: hubUrl })
    const page = await context.newPage()
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)

      await installObservers(page)

      const anchor = await page.evaluate(() => ({ perf: performance.now(), wall: Date.now() }))
      const logsBefore = srv.logLines.length
      await page.locator('[data-testid="tab"][data-tab-type="agent"]')
        .locator('[data-testid="tab-close"]')
        .dispatchEvent('click')

      await expect(page.getByRole('heading', { name: 'Close Last Tab' })).toBeVisible()
      const tDialogClickMs = await page.evaluate(() => performance.now())
      await page.getByRole('button', { name: 'Close anyway' }).click()
      await page.getByRole('button', { name: 'Confirm?' }).click()

      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(0)
      const raw = await captureCloseTimeline(
        page,
        srv,
        logsBefore,
        anchor,
        testInfo,
        'scenario 2: worktree close-anyway (KEEP)',
        'close-timing-scenario-2',
        [{ name: 'ui:dialog-user-click', tMs: tDialogClickMs }],
      )

      expect(raw.dialogVisibleAt).not.toBeNull()
      expect(raw.tabRemovedAt).not.toBeNull()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
      await context.close()
    }
  })

  test('scenario 3 — worktree close with "Delete" (REMOVE)', async ({ browser }, testInfo) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = srv
    const ctx = getRepoCtx(dataDir, 'close-timing-scn3')
    const worktreeDir = ctx.synthetic
      ? join(realpathSync(dataDir), 'close-timing-scn3-worktrees', 'scn3-branch')
      : null

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'close-timing-scn3',
      adminOrgId,
      ctx.repoDir,
      'scn3-branch',
    )
    if (worktreeDir) {
      await expect.poll(() => existsSync(worktreeDir), { timeout: 15_000, intervals: [100] }).toBe(true)
    }

    const context = await browser.newContext({ baseURL: hubUrl })
    const page = await context.newPage()
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)

      await installObservers(page)

      const anchor = await page.evaluate(() => ({ perf: performance.now(), wall: Date.now() }))
      const logsBefore = srv.logLines.length
      await page.locator('[data-testid="tab"][data-tab-type="agent"]')
        .locator('[data-testid="tab-close"]')
        .dispatchEvent('click')

      await expect(page.getByRole('heading', { name: 'Close Last Tab' })).toBeVisible()
      const tDialogClickMs = await page.evaluate(() => performance.now())
      await page.getByRole('button', { name: 'Delete' }).click()
      await page.getByRole('button', { name: 'Confirm?' }).click()

      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(0)
      const raw = await captureCloseTimeline(
        page,
        srv,
        logsBefore,
        anchor,
        testInfo,
        'scenario 3: worktree delete (REMOVE)',
        'close-timing-scenario-3',
        [{ name: 'ui:dialog-user-click', tMs: tDialogClickMs }],
      )

      expect(raw.dialogVisibleAt).not.toBeNull()
      expect(raw.tabRemovedAt).not.toBeNull()
      if (worktreeDir)
        await waitForPathDeleted(worktreeDir, 10_000)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
      await context.close()
    }
  })
})
