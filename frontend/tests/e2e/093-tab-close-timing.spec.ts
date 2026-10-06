/* eslint-disable no-console */
/**
 * Measure tab close with browser events and a traced private Worker.
 * The suite Hub serves all three cases. Each case uses a fresh workspace.
 * LEAPMUX_CLOSE_TIMING_REPO_DIR selects a real repository instead of a private test repository.
 */
import type { Page, TestInfo } from '@playwright/test'
import type { ClockAnchor, LogLine, PhaseMark, RpcMark, TimingWorker } from './helpers/timingFixture'
import process from 'node:process'
import { expect } from '@playwright/test'
import { test as base } from './fixtures'
import { deleteWorkspaceViaAPI } from './helpers/api'
import { withCleanup } from './helpers/cleanup'
import { extractWorkerMarks, installRpcListeners, renderTimeline, withTimingWorker } from './helpers/timingFixture'
import { AGENT_TAB_SELECTOR, agentTabs, expectAgentTabCount, loginViaToken, openAgentViaUI, openWorkspace } from './helpers/ui'
import {
  createGitRepo,
  createWorkspaceWithWorktreeViaAPI,
  waitForPathDeleted,
} from './helpers/worktree'

// ─── Browser instrumentation ──────────────────────────────────────────

/** The RPC that closes a tab: CloseAgent for an agent tab, CloseTerminal for a terminal tab. */
const CLOSE_RPC_METHOD = /^Close(?:Agent|Terminal)$/

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
  await page.evaluate((agentTabSelector) => {
    const w: Window & TimingWindow = window
    w.__tabRemovedAt = null
    w.__dialogVisibleAt = null
    w.__dialogRemovedAt = null
    w.__tabBaseline = document.querySelectorAll(agentTabSelector).length
    w.__observer?.disconnect()
    w.__observer = new MutationObserver(() => {
      if (w.__tabRemovedAt == null) {
        const tabs = document.querySelectorAll(agentTabSelector).length
        if (tabs < (w.__tabBaseline ?? 0))
          w.__tabRemovedAt = performance.now()
      }
      if (w.__dialogVisibleAt == null) {
        const dialog = document.querySelector('dialog[open]')
        // Compare the dialog text without case differences. Its heading uses sentence case.
        if (dialog && dialog.textContent?.toLowerCase().includes('close last tab'))
          w.__dialogVisibleAt = performance.now()
      }
      if (w.__dialogVisibleAt != null && w.__dialogRemovedAt == null) {
        const dialog = document.querySelector('dialog[open]')
        if (!dialog || !dialog.textContent?.toLowerCase().includes('close last tab'))
          w.__dialogRemovedAt = performance.now()
      }
    })
    w.__observer.observe(document.body, { childList: true, subtree: true })
  }, AGENT_TAB_SELECTOR)
}

interface RawMarks {
  rpcMarks: RpcMark[]
  tabRemovedAt: number | null
  dialogVisibleAt: number | null
  dialogRemovedAt: number | null
}

async function snapshotMarks(page: Page): Promise<RawMarks> {
  return page.evaluate(() => {
    const w: Window & TimingWindow = window
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

// Wait for the close RPC reply and the Worker's handler_begin marker.
// Attach the combined timeline and return the browser marks for each case's assertions.
async function captureCloseTimeline(
  page: Page,
  srv: Pick<TimingWorker, 'logLines'>,
  logsBefore: number,
  anchor: ClockAnchor,
  testInfo: TestInfo,
  scenarioLabel: string,
  attachName: string,
  extra: PhaseMark[] = [],
): Promise<RawMarks> {
  // Poll the marks themselves, so a timeout prints what the browser reported.
  // An empty list means that the served frontend emits no RPC marks at all: it
  // was built without LEAPMUX_DEV=1.
  await expect.poll(async () => (await snapshotMarks(page)).rpcMarks)
    .toContainEqual(expect.objectContaining({ type: 'rpc-recv', method: expect.stringMatching(CLOSE_RPC_METHOD) }))
  await expect.poll(() => findClosedTabID(srv.logLines, logsBefore) !== null).toBeTruthy()
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

const test = base.extend<{ timingWorker: TimingWorker }>({
  timingWorker: async ({ leapmuxServer }, use) => withTimingWorker(leapmuxServer, {
    dataDirPrefix: 'leapmux-close-timing-e2e',
    env: { LEAPMUX_TRACE_TAB_CLOSE: '1' },
  }, use),
})

test.describe('Tab close timing', () => {
  test.describe.configure({ retries: 0 })

  test('scenario 1 — two worktree tabs on the same worktree, close one', async ({ page, timingWorker }, testInfo) => {
    const srv = { ...timingWorker.server, logLines: timingWorker.logLines, dataDir: timingWorker.dataDir }
    const { hubUrl, adminToken, workerId, dataDir } = srv
    const ctx = getRepoCtx(dataDir, 'close-timing-scn1')

    // The first agent creates scn1-branch. The second agent reuses the active worktree.
    // Closing one agent leaves another tab, so the Worker returns shouldPrompt=false.
    const { workspaceId } = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'close-timing-scn1',
      ctx.repoDir,
      'scn1-branch',
    )

    await withCleanup(async () => {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)
      await expectAgentTabCount(page, 1)
      // The helper waits for the tab directory before it clicks, and for the new
      // tab and its composer after the click.
      await openAgentViaUI(page)
      await expectAgentTabCount(page, 2)

      await installObservers(page)

      const anchor = await page.evaluate(() => ({ perf: performance.now(), wall: Date.now() }))
      const logsBefore = srv.logLines.length
      await agentTabs(page).first().locator('[data-testid="tab-close"]').dispatchEvent('click')

      await expectAgentTabCount(page, 1)
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
    }, () => deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId))
  })

  test('scenario 2 — worktree close with "Close anyway" (KEEP)', async ({ page, timingWorker }, testInfo) => {
    const srv = { ...timingWorker.server, logLines: timingWorker.logLines, dataDir: timingWorker.dataDir }
    const { hubUrl, adminToken, workerId, dataDir } = srv
    const ctx = getRepoCtx(dataDir, 'close-timing-scn2')

    const { workspaceId } = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'close-timing-scn2',
      ctx.repoDir,
      'scn2-branch',
    )

    await withCleanup(async () => {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)
      await expectAgentTabCount(page, 1)

      await installObservers(page)

      const anchor = await page.evaluate(() => ({ perf: performance.now(), wall: Date.now() }))
      const logsBefore = srv.logLines.length
      await agentTabs(page)
        .locator('[data-testid="tab-close"]')
        .dispatchEvent('click')

      await expect(page.getByRole('heading', { name: 'Close Last Tab' })).toBeVisible()
      const tDialogClickMs = await page.evaluate(() => performance.now())
      await page.getByRole('button', { name: 'Close anyway' }).click()
      await page.getByRole('button', { name: 'Confirm?' }).click()

      await expectAgentTabCount(page, 0)
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
    }, () => deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId))
  })

  test('scenario 3 — worktree close with "Delete" (REMOVE)', async ({ page, timingWorker }, testInfo) => {
    const srv = { ...timingWorker.server, logLines: timingWorker.logLines, dataDir: timingWorker.dataDir }
    const { hubUrl, adminToken, workerId, dataDir } = srv
    const ctx = getRepoCtx(dataDir, 'close-timing-scn3')

    // The helper returns once the worktree exists on disk.
    const { workspaceId, worktreeDir: createdWorktreeDir } = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'close-timing-scn3',
      ctx.repoDir,
      'scn3-branch',
    )
    // The removal is checked only in a repository that the test owns.
    const worktreeDir = ctx.synthetic ? createdWorktreeDir : null
    await withCleanup(async () => {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)
      await expectAgentTabCount(page, 1)

      await installObservers(page)

      const anchor = await page.evaluate(() => ({ perf: performance.now(), wall: Date.now() }))
      const logsBefore = srv.logLines.length
      await agentTabs(page)
        .locator('[data-testid="tab-close"]')
        .dispatchEvent('click')

      await expect(page.getByRole('heading', { name: 'Close Last Tab' })).toBeVisible()
      const tDialogClickMs = await page.evaluate(() => performance.now())
      await page.getByRole('button', { name: 'Delete worktree' }).click()
      await page.getByRole('button', { name: 'Confirm?' }).click()

      await expectAgentTabCount(page, 0)
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
        await waitForPathDeleted(worktreeDir)
    }, () => deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId))
  })
})
