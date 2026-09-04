import type { Page } from '@playwright/test'
import { execSync } from 'node:child_process'
import { existsSync, mkdirSync, realpathSync, writeFileSync } from 'node:fs'
import { basename, dirname, join } from 'node:path'
import {
  AgentStatus,
  CloseAgentRequestSchema,
  CloseAgentResponseSchema,
  ListAgentsRequestSchema,
  ListAgentsResponseSchema,
} from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { WorktreeAction } from '../../../src/generated/proto/leapmux/v1/common_pb'
import {
  InspectLastTabCloseRequestSchema,
  InspectLastTabCloseResponseSchema,
  PushBranchRequestSchema,
  PushBranchResponseSchema,
} from '../../../src/generated/proto/leapmux/v1/git_pb'
import {
  CloseTerminalRequestSchema,
  CloseTerminalResponseSchema,
  ListTerminalsRequestSchema,
  ListTerminalsResponseSchema,
} from '../../../src/generated/proto/leapmux/v1/terminal_pb'
import { expect } from '../fixtures'
import { API_POLL_INTERVAL_MS, authedHeaders, createWorkspaceViaAPI, getTestChannel, openAgentViaAPI } from './api'
import { expectAnyVisible, isMaybeVisible } from './ui'

/**
 * Create a git repo inside the server's data directory so the worker can access it.
 *
 * The repo pins three settings that would otherwise be inherited from the
 * machine's global gitconfig, because each of them puts a SECOND writer inside
 * `.git` that the test never asked for:
 *
 * - `core.fsmonitor` (commonly on for macOS dev machines) makes git spawn a
 *   filesystem-monitor daemon per repo. It outlives the command that started
 *   it, touches the index, and leaves a unix socket behind -- so it competes
 *   for `index.lock` with the worker's own git commands and survives into the
 *   test's cleanup.
 * - `gc.auto` / `maintenance.auto` are the same hazard in slower motion: a
 *   commit can fire a background `git gc` that writes after the command
 *   returns.
 *
 * `--initial-branch=main` is pinned for the same reason it is in the Go
 * helpers: a host without `init.defaultBranch` lands on `master`, and the
 * specs address the initial branch by name.
 */
export function createGitRepo(dataDir: string, name: string): string {
  const repoDir = join(dataDir, name)
  mkdirSync(repoDir, { recursive: true })
  execSync('git -c core.fsmonitor=false init --initial-branch=main', { cwd: repoDir })
  execSync('git config core.fsmonitor false', { cwd: repoDir })
  execSync('git config gc.auto 0', { cwd: repoDir })
  execSync('git config maintenance.auto false', { cwd: repoDir })
  execSync('git config user.email "test@test.com"', { cwd: repoDir })
  execSync('git config user.name "Test"', { cwd: repoDir })
  writeFileSync(join(repoDir, 'README.md'), '# Test\n')
  execSync('git add .', { cwd: repoDir })
  execSync('git commit -m "init"', { cwd: repoDir })
  return repoDir
}

/**
 * Check if a git branch exists in a repository.
 */
export function branchExists(repoDir: string, branchName: string): boolean {
  const output = execSync(`git branch --list ${branchName}`, { cwd: repoDir }).toString().trim()
  return output.length > 0
}

/**
 * How long a filesystem effect of a worker-side git operation may take.
 *
 * Deliberately far below the suite's 120s assertion budget, and measured rather
 * than guessed: every SUCCESSFUL worktree create/remove in these specs lands
 * within a second or two even with eight workers competing for git, while the
 * failures observed under load never complete at all -- the add dies on a stale
 * index.lock and nothing ever appears. So a long budget buys no passes and
 * costs minutes of wall time per failing test. 30s is roughly an order of
 * magnitude of headroom over the slowest success seen.
 */
const GIT_FS_EFFECT_TIMEOUT_MS = 30_000

/**
 * Poll until a path exists on disk (worktree creation is async).
 *
 * The RPC that creates a worktree returns once the worker has accepted the
 * request; the `git worktree add` runs on the startup goroutine afterwards, so
 * a one-shot `existsSync` right after the dialog closes is a coin flip.
 */
export async function waitForPathExists(path: string): Promise<void> {
  await expect(() => {
    expect(existsSync(path), `path should exist: ${path}`).toBe(true)
  }).toPass({ timeout: GIT_FS_EFFECT_TIMEOUT_MS })
}

/**
 * Poll until `repoDir` has `branch` checked out.
 *
 * Every git-mode checkout runs on the worker's async startup goroutine, which
 * starts only after OpenAgent has answered -- so by the time the RPC returns or
 * the create-workspace dialog closes, HEAD has usually not moved yet. A one-shot
 * `git rev-parse` there is a race that reads `main` and reports the feature as
 * broken. Shared so the UI and API variants of the same assertion cannot drift:
 * the API one already polled, the UI one did not, and only the UI one flaked.
 */
export async function expectRepoBranch(repoDir: string, branch: string): Promise<void> {
  await expect
    .poll(() => execSync('git rev-parse --abbrev-ref HEAD', { cwd: repoDir }).toString().trim())
    .toBe(branch)
}

/**
 * Poll until a path no longer exists on disk (worktree removal is async).
 * Same budget rationale as {@link waitForPathExists}.
 */
export async function waitForPathDeleted(path: string): Promise<void> {
  await expect(() => {
    expect(existsSync(path), `path should be gone: ${path}`).toBe(false)
  }).toPass({ timeout: GIT_FS_EFFECT_TIMEOUT_MS })
}

/**
 * Wait for the app home to be ready (sidebar sections loaded).
 * Unlike waitForWorkspaceReady, this works on non-workspace routes like /.
 */
export async function waitForAppPageReady(page: Page) {
  await expect(page.locator('[data-testid="section-header-workspaces_in_progress"]')).toBeVisible()
}

/**
 * Open the "New Workspace" dialog by whichever route is available.
 *
 * The In-progress section header carries a MENU now, and "New workspace..." is
 * an item inside it -- so the availability probe has to read the TRIGGER, not
 * the item. A closed `popover="auto"` is `display: none`, so probing the item
 * would answer "not visible" every single time, send every caller down the
 * empty-state fallback, and time out wherever that button does not exist.
 *
 * The left sidebar may still be collapsed (rail mode), which hides the section
 * header entirely; the empty-state `create-workspace-button` is the fallback
 * for that.
 *
 * Open-and-click is retried as one unit, following `clickRowMenuItem`: the
 * sidebar re-renders on workspace, worker and todo changes, so the menu can
 * vanish between opening it and clicking inside it.
 */
export async function openNewWorkspaceDialog(page: Page) {
  const sectionMenu = page.locator('[data-testid="sidebar-section-menu-workspaces_in_progress"]')
  const createBtn = page.locator('[data-testid="create-workspace-button"]')
  await expectAnyVisible(sectionMenu, createBtn)
  if (await isMaybeVisible(sectionMenu)) {
    const item = page.locator('[data-testid="sidebar-new-workspace"]:visible')
    await expect(async () => {
      // Idempotent open, the way `ensureExpanded` does it: a second click on an
      // already-open trigger CLOSES the menu, which is exactly what a naive
      // retry would do.
      if (!await item.isVisible())
        await sectionMenu.click()
      await expect(item).toBeVisible()
      await item.click()
    }).toPass()
  }
  else {
    await createBtn.click()
  }
  await expect(page.getByRole('heading', { name: 'New Workspace' })).toBeVisible()
}

/**
 * Open the "New Agent" dialog from within a workspace via the tab menu.
 */
export async function openNewAgentDialog(page: Page) {
  const addMenu = page.locator('[data-testid="tab-more-menu"]').first()
  await addMenu.click()
  await page.getByRole('menuitem', { name: 'New agent...' }).click()
  await expect(page.getByRole('heading', { name: 'New Agent' })).toBeVisible()
}

/**
 * Create a workspace on the hub, then open an agent with worktree enabled.
 * Returns the workspace ID.
 */
export async function createWorkspaceWithWorktreeViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  title: string,
  workingDir: string,
  worktreeBranch: string,
): Promise<string> {
  const workspaceId = await createWorkspaceViaAPI(hubUrl, token, title)
  await openAgentViaAPI(hubUrl, token, workerId, workspaceId, workingDir, {
    createWorktree: true,
    worktreeBranch,
  })

  // OpenAgent returns synchronously with status=STARTING and the worktree is
  // created asynchronously during phased startup (#194). Tests expect
  // `existsSync(worktreeDir)` to be true immediately after this helper returns,
  // so wait until the worker has actually materialized it on disk.
  //
  // The path is anchored on the MAIN REPO ROOT, matching the worker's placement
  // convention. Deriving it from `workingDir` (as this did) is wrong whenever the
  // working dir is itself a linked worktree: the worker still places the new
  // worktree beside the main repo, so the helper waited out its full timeout on a
  // path that never appears.
  const repoRoot = mainRepoRoot(workingDir)
  const expectedWorktreeDir = join(dirname(repoRoot), `${basename(repoRoot)}-worktrees`, worktreeBranch)
  const deadline = Date.now() + 30_000
  while (!existsSync(expectedWorktreeDir)) {
    if (Date.now() > deadline) {
      throw new Error(
        `createWorkspaceWithWorktreeViaAPI: worktree did not appear at ${expectedWorktreeDir} within 30s`,
      )
    }
    await new Promise(r => setTimeout(r, 25))
  }

  return workspaceId
}

/**
 * The MAIN repository's root for `dir`, even when `dir` is a linked worktree.
 *
 * `--show-toplevel` would answer the worktree's own root, which is exactly the
 * trap this exists to avoid. `--git-common-dir` resolves to the main repo's
 * `.git` from any worktree, so its parent is the main root.
 */
function mainRepoRoot(dir: string): string {
  const commonDir = execSync('git rev-parse --path-format=absolute --git-common-dir', { cwd: dir })
    .toString()
    .trim()
  return realpathSync(dirname(commonDir))
}

/**
 * Wait until the workspace holds `expectedCount` agents and none is still
 * AGENT_STATUS_STARTING.
 *
 * `waitForAgentsViaAPI` waits only for an agent to APPEAR, which OpenAgent
 * satisfies as soon as the DB row exists. The git-mode work (creating a worktree,
 * checking a branch out) and the `worktree_tabs` registration both happen on the
 * async startup goroutine AFTER that. So a test that acts on those effects --
 * reading the branch off disk, or closing a sibling tab and expecting the
 * worktree to still be referenced -- has to wait for startup, not for arrival.
 * Reading immediately is a race that fails more often than it passes.
 */
export async function waitForAgentStartupViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  workspaceId: string,
  expectedCount = 1,
  timeoutMs = 30_000,
  intervalMs = API_POLL_INTERVAL_MS,
): Promise<Array<{ id: string, title: string, workingDir: string, status: number, startupError: string }>> {
  const deadline = Date.now() + timeoutMs
  while (true) {
    const agents = await listAgentsViaAPI(hubUrl, token, workerId, workspaceId)
    // A FAILED startup is terminal, so waiting longer cannot help -- and it is
    // the interesting case: the git-mode work is what failed, so every
    // downstream assertion (the worktree exists, the branch is checked out)
    // would report a confusing false instead of the worker's actual error.
    const failed = agents.filter(a => a.status === AgentStatus.STARTUP_FAILED)
    if (failed.length > 0) {
      throw new Error(
        `waitForAgentStartupViaAPI: agent startup failed: ${failed.map(a => `${a.id}: ${a.startupError || '(no startup_error reported)'}`).join('; ')}`,
      )
    }
    if (agents.length >= expectedCount && agents.every(a => a.status !== AgentStatus.STARTING)) {
      return agents
    }
    if (Date.now() >= deadline) {
      throw new Error(
        `waitForAgentStartupViaAPI: ${expectedCount} agent(s) did not finish starting within ${timeoutMs}ms `
        + `(saw ${JSON.stringify(agents)})`,
      )
    }
    await new Promise(r => setTimeout(r, intervalMs))
  }
}

/**
 * Close a terminal via E2EE channel. Pass `worktreeAction` to atomically
 * remove the worktree after the PTY/DB cleanup (REMOVE) or keep it (KEEP).
 */
export async function closeTerminalViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  terminalId: string,
  worktreeAction: WorktreeAction = WorktreeAction.KEEP,
): Promise<{ worktreePath: string, worktreeId: string, failureMessage: string, failureDetail: string }> {
  const channel = await getTestChannel(hubUrl, token)
  const resp = await channel.callWorker(
    workerId,
    'CloseTerminal',
    CloseTerminalRequestSchema,
    CloseTerminalResponseSchema,
    { terminalId, worktreeAction },
  )
  const result = resp.result
  return {
    worktreePath: result?.worktreePath ?? '',
    worktreeId: result?.worktreeId ?? '',
    failureMessage: result?.failureMessage ?? '',
    failureDetail: result?.failureDetail ?? '',
  }
}

/**
 * Close an agent via E2EE channel. Pass `worktreeAction` to atomically
 * remove the worktree after the process/DB cleanup (REMOVE) or keep it
 * (KEEP).
 */
export async function closeAgentViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  agentId: string,
  worktreeAction: WorktreeAction = WorktreeAction.KEEP,
): Promise<{ worktreePath: string, worktreeId: string, failureMessage: string, failureDetail: string }> {
  const channel = await getTestChannel(hubUrl, token)
  const resp = await channel.callWorker(
    workerId,
    'CloseAgent',
    CloseAgentRequestSchema,
    CloseAgentResponseSchema,
    { agentId, worktreeAction },
  )
  const result = resp.result
  return {
    worktreePath: result?.worktreePath ?? '',
    worktreeId: result?.worktreeId ?? '',
    failureMessage: result?.failureMessage ?? '',
    failureDetail: result?.failureDetail ?? '',
  }
}

/**
 * Poll `listAgentsViaAPI` until at least one agent is returned or the
 * timeout elapses.  Call this instead of `listAgentsViaAPI` directly when
 * the agent was just created via the UI or an API call that may not have
 * been persisted by the backend yet.
 */
export async function waitForAgentsViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  workspaceId: string,
  timeoutMs = 15_000,
  intervalMs = API_POLL_INTERVAL_MS,
): Promise<Array<{ id: string, title: string, workingDir: string, status: number, startupError: string }>> {
  const deadline = Date.now() + timeoutMs
  while (true) {
    const agents = await listAgentsViaAPI(hubUrl, token, workerId, workspaceId)
    if (agents.length > 0) {
      return agents
    }
    if (Date.now() >= deadline) {
      throw new Error(`No agents appeared for workspace ${workspaceId} within ${timeoutMs}ms`)
    }
    await new Promise(r => setTimeout(r, intervalMs))
  }
}

/**
 * List agents for a workspace via hub ListTabs + worker ListAgents.
 * The ListAgents RPC now accepts tab_ids instead of workspace_id,
 * so we first fetch the tab list from the hub and then request agents by ID.
 */
export async function listAgentsViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  workspaceId: string,
): Promise<Array<{ id: string, title: string, workingDir: string, status: number, startupError: string }>> {
  // Get tab IDs from the hub's ListTabs endpoint.
  const tabsRes = await fetch(`${hubUrl}/leapmux.v1.WorkspaceService/ListTabs`, {
    method: 'POST',
    headers: authedHeaders(token),
    body: JSON.stringify({ workspaceIds: [workspaceId] }),
  })
  if (!tabsRes.ok) {
    throw new Error(`ListTabs failed: ${tabsRes.status}`)
  }
  const tabsData = await tabsRes.json() as { tabs?: Array<{ tabType: string, tabId: string }> }
  const agentTabIds = (tabsData.tabs ?? [])
    .filter(t => t.tabType === 'TAB_TYPE_AGENT')
    .map(t => t.tabId)

  if (agentTabIds.length === 0) {
    return []
  }

  const channel = await getTestChannel(hubUrl, token)
  let resp: Awaited<ReturnType<typeof channel.callWorker<typeof ListAgentsRequestSchema, typeof ListAgentsResponseSchema>>>
  try {
    resp = await channel.callWorker(
      workerId,
      'ListAgents',
      ListAgentsRequestSchema,
      ListAgentsResponseSchema,
      { tabIds: agentTabIds },
    )
  }
  catch {
    // Treat as transient; caller retries via waitForAgentsViaAPI.
    return []
  }
  return (resp.agents ?? []).map(a => ({ id: a.id, title: a.title, workingDir: a.workingDir, status: a.status, startupError: a.startupError }))
}

/**
 * List a workspace's terminals via hub ListTabs + worker ListTerminals.
 *
 * The worker's DB is the only durable home of a terminal's title, so this is
 * where a test asks whether a rename PERSISTED. The tab bar shows a rename
 * immediately -- the handler patches local metadata and fires
 * `UpdateTerminalTitle` without awaiting it -- so a reload or a worker restart
 * begun in that window drops the write and the failure reads as a
 * persistence regression. Sibling of `listAgentsViaAPI`, same two-step shape.
 */
export async function listTerminalsViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  workspaceId: string,
): Promise<Array<{ id: string, title: string, status: number, exited: boolean }>> {
  const tabsRes = await fetch(`${hubUrl}/leapmux.v1.WorkspaceService/ListTabs`, {
    method: 'POST',
    headers: authedHeaders(token),
    body: JSON.stringify({ workspaceIds: [workspaceId] }),
  })
  if (!tabsRes.ok) {
    throw new Error(`ListTabs failed: ${tabsRes.status}`)
  }
  const tabsData = await tabsRes.json() as { tabs?: Array<{ tabType: string, tabId: string }> }
  const terminalTabIds = (tabsData.tabs ?? [])
    .filter(t => t.tabType === 'TAB_TYPE_TERMINAL')
    .map(t => t.tabId)

  if (terminalTabIds.length === 0) {
    return []
  }

  const channel = await getTestChannel(hubUrl, token)
  try {
    const resp = await channel.callWorker(
      workerId,
      'ListTerminals',
      ListTerminalsRequestSchema,
      ListTerminalsResponseSchema,
      { tabIds: terminalTabIds },
    )
    return (resp.terminals ?? []).map(t => ({
      id: t.terminalId,
      title: t.title,
      status: t.status,
      exited: t.exited,
    }))
  }
  catch {
    // Treat as transient so a caller polling this converges instead of
    // failing on one blip.
    return []
  }
}

/**
 * Inspect the last-tab close state via E2EE channel.
 */
export async function inspectLastTabCloseViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  tabType: number,
  tabId: string,
): Promise<{
  target: number
  shouldPrompt: boolean
  worktreePath: string
  worktreeId: string
  branchName: string
  canPush: boolean
  hasUncommittedChanges: boolean
  unpushedCommitCount: number
  remoteBranchMissing: boolean
}> {
  const channel = await getTestChannel(hubUrl, token)
  const resp = await channel.callWorker(
    workerId,
    'InspectLastTabClose',
    InspectLastTabCloseRequestSchema,
    InspectLastTabCloseResponseSchema,
    { tabType, tabId },
  )
  const gs = resp.gitState
  return {
    target: resp.target,
    shouldPrompt: resp.shouldPrompt,
    worktreePath: resp.worktreePath,
    worktreeId: resp.worktreeId,
    branchName: resp.branchName,
    canPush: gs?.canPush ?? false,
    hasUncommittedChanges: gs?.hasUncommittedChanges ?? false,
    unpushedCommitCount: gs?.unpushedCommitCount ?? 0,
    remoteBranchMissing: gs?.remoteBranchMissing ?? false,
  }
}

/**
 * Push or commit-and-push the branch a tab lives on, via E2EE channel.
 */
export async function pushBranchViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  workingDir: string,
): Promise<void> {
  const channel = await getTestChannel(hubUrl, token)
  await channel.callWorker(
    workerId,
    'PushBranch',
    PushBranchRequestSchema,
    PushBranchResponseSchema,
    { workingDir },
  )
}

/** Wait for a worker to be available (retry with backoff). */
export async function waitForWorker(page: Page) {
  const dialog = page.getByRole('dialog')
  // The worker picker is a menu. Its TRIGGER shows only the selected worker,
  // where the `<select>` this replaced held every option's text at once -- so
  // this reads the trigger, which is the worker the dialog would actually use.
  const workerSelect = dialog.getByTestId('worker-select-menu-trigger')
  const refreshBtn = dialog.getByLabel('Refresh workers')
  for (let attempt = 0; attempt < 6; attempt++) {
    try {
      await expect(workerSelect).toContainText('Local')
      break
    }
    catch {
      if (attempt === 5)
        throw new Error('No online worker found')
      await refreshBtn.click()
    }
  }
}

/**
 * Set the working directory in a dialog by filling the path input and pressing Enter.
 * SolidJS uses event delegation (document-level listeners keyed by `$$eventType`).
 * Playwright's fill() sets el.value directly but may not trigger a bubbling InputEvent
 * that SolidJS's delegation picks up. We dispatch a real InputEvent manually to ensure
 * the SolidJS signal updates before pressing Enter.
 */
export async function setWorkingDir(page: Page, dirPath: string) {
  const dialog = page.getByRole('dialog')
  const pathInput = dialog.getByPlaceholder('Enter path...')
  await pathInput.click()
  await pathInput.evaluate((el: HTMLInputElement, value: string) => {
    el.value = value
    el.dispatchEvent(new InputEvent('input', { bubbles: true }))
  }, dirPath)
  await pathInput.press('Enter')
}
