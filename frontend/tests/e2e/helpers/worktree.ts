import type { Page } from '@playwright/test'
import type { AgentServer } from './workspace'
import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, realpathSync, writeFileSync } from 'node:fs'
import { basename, dirname, join } from 'node:path'
import { expect } from '@playwright/test'
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
import { API_POLL_INTERVAL_MS, callHub, createWorkspaceViaAPI, getTestChannel, openAgentViaAPI } from './api'
import { retryUntilPass } from './retryUntilPass'
import { waitTimeoutBeforeTestDeadline } from './testDeadline'
import { activeWorkspaceRow, expectAnyVisible, isMaybeVisible, loginViaToken, sidebarSectionHeader } from './ui'

/** Run one git command in `cwd` with no shell, so a path or a name reaches git as one argument. */
function git(cwd: string, args: string[]): string {
  return execFileSync('git', args, { cwd, encoding: 'utf8' })
}

/**
 * The settings that every test repository pins, so that the machine's global gitconfig cannot change them.
 *
 * Each of the first three would otherwise put a SECOND writer inside `.git` that the test never asked for:
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
 * The identity lets a test commit on a host with no global `user.name`.
 * A linked worktree reads the config of its main repository, so it needs no copy of these settings.
 */
const PINNED_GIT_CONFIG: ReadonlyArray<readonly [string, string]> = [
  ['core.fsmonitor', 'false'],
  ['gc.auto', '0'],
  ['maintenance.auto', 'false'],
  ['user.email', 'test@test.com'],
  ['user.name', 'Test'],
]

/** Write the pinned settings into the config of the repository at `repoDir`. */
function pinGitConfig(repoDir: string): void {
  for (const [key, value] of PINNED_GIT_CONFIG)
    git(repoDir, ['config', key, value])
}

/**
 * Initialize an empty repository at `dir` on branch `main`, with the pinned settings.
 *
 * `--initial-branch=main` is pinned for the same reason it is in the Go
 * helpers: a host without `init.defaultBranch` lands on `master`, and the
 * specs address the initial branch by name. The init itself runs with
 * `core.fsmonitor=false`, because no repository config exists yet to pin it.
 */
export function initGitRepo(dir: string): void {
  mkdirSync(dir, { recursive: true })
  git(dir, ['-c', 'core.fsmonitor=false', 'init', '--initial-branch=main'])
  pinGitConfig(dir)
}

/**
 * Write `content` to `path` inside the repository, stage that file, and commit it with `message`.
 * The parent directories of `path` are created first.
 */
export function commitFile(repoDir: string, path: string, content: string, message: string): void {
  const file = join(repoDir, path)
  mkdirSync(dirname(file), { recursive: true })
  writeFileSync(file, content)
  git(repoDir, ['add', '--', path])
  git(repoDir, ['commit', '-m', message])
}

/**
 * Create a git repo inside the server's data directory so the worker can access it.
 * The repository holds one commit of `README.md` on `main`, and carries the pinned settings of `initGitRepo`.
 */
export function createGitRepo(dataDir: string, name: string): string {
  const repoDir = join(dataDir, name)
  initGitRepo(repoDir)
  commitFile(repoDir, 'README.md', '# Test\n', 'init')
  return repoDir
}

/**
 * Create a bare repository as a remote, and a clone of it whose `main` holds one pushed commit, so the clone's
 * branch has an upstream. The bare repository is `<dataDir>/<name>-bare`, and the clone is `<dataDir>/<name>`.
 * Both carry the pinned settings.
 */
export function createGitRepoWithRemote(dataDir: string, name: string): { repoDir: string, bareDir: string } {
  const bareDir = join(dataDir, `${name}-bare`)
  mkdirSync(bareDir, { recursive: true })
  git(bareDir, ['-c', 'core.fsmonitor=false', 'init', '--bare', '--initial-branch=main'])
  pinGitConfig(bareDir)
  const repoDir = join(dataDir, name)
  git(dataDir, ['-c', 'core.fsmonitor=false', 'clone', bareDir, repoDir])
  // The clone of an empty repository takes its unborn branch from the host's `init.defaultBranch`, not from the
  // remote, so the branch is named here.
  git(repoDir, ['symbolic-ref', 'HEAD', 'refs/heads/main'])
  pinGitConfig(repoDir)
  commitFile(repoDir, 'README.md', '# Test\n', 'init')
  git(repoDir, ['push', '-u', 'origin', 'HEAD'])
  return { repoDir, bareDir }
}

/**
 * Add a linked worktree of `repoDir` at `<dataDir>/<name>` on the new branch `branch`, and return its real path.
 * The real path, because the Worker reports a resolved path: the temporary directory of macOS is a symlink
 * (`/var` to `/private/var`).
 */
export function addWorktree(repoDir: string, dataDir: string, name: string, branch: string): string {
  const target = join(dataDir, name)
  git(repoDir, ['worktree', 'add', target, '-b', branch])
  return realpathSync(target)
}

/**
 * Check if a git branch exists in a repository.
 * The check reads the exact ref `refs/heads/<branchName>`. A `git branch --list` lookup treats the name as a pattern,
 * and git reads a name that starts with `-` as an option.
 */
export function branchExists(repoDir: string, branchName: string): boolean {
  try {
    git(repoDir, ['show-ref', '--verify', '--quiet', `refs/heads/${branchName}`])
    return true
  }
  catch (error) {
    // show-ref exits with 1 for an absent ref. Another status is a real failure, such as a directory that is not a repository.
    if ((error as { status?: unknown }).status === 1)
      return false
    throw error
  }
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
    .poll(() => git(repoDir, ['rev-parse', '--abbrev-ref', 'HEAD']).trim())
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
  await expect(sidebarSectionHeader(page, 'workspaces_in_progress')).toBeVisible()
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
  await openTabMenuDialog(page, 'New agent...', 'New Agent')
}

/**
 * Open the "New Terminal" dialog from within a workspace via the tab menu.
 */
export async function openNewTerminalDialog(page: Page) {
  await openTabMenuDialog(page, 'New terminal...', 'New Terminal')
}

/** Click one item of the tab bar's `+` menu, and wait for the heading of the dialog that it opens. */
async function openTabMenuDialog(page: Page, item: string, heading: string): Promise<void> {
  await page.locator('[data-testid="tab-more-menu"]').first().click()
  await page.getByRole('menuitem', { name: item }).click()
  await expect(page.getByRole('heading', { name: heading })).toBeVisible()
}

/**
 * Sign in, open the app home, open the New Workspace dialog, wait for a Worker, and set the working directory to
 * `dir`. The dialog then shows the git options of `dir`.
 */
export async function openNewWorkspaceDialogAt(page: Page, token: string, dir: string): Promise<void> {
  await loginViaToken(page, token)
  await page.goto('/')
  await waitForAppPageReady(page)
  await openNewWorkspaceDialog(page)
  await waitForWorker(page)
  await setWorkingDir(page, dir)
}

/**
 * Replace the title that the New Workspace dialog generates with `title`.
 * By ROLE and NAME, not by placeholder. The placeholder became "Type a name" when the dialog started to generate a
 * title, so a lookup by the old placeholder matches nothing and waits out its whole timeout. `fill` replaces the
 * generated name.
 */
export async function fillWorkspaceTitle(page: Page, title: string): Promise<void> {
  await page.getByRole('dialog').getByRole('textbox', { name: 'Title' }).fill(title)
}

/**
 * Select one git mode of the open dialog by its label, such as "Create new worktree".
 * The options appear only after the Worker reports the git state of the working directory, so the label is awaited first.
 */
export async function chooseGitMode(page: Page, label: string): Promise<void> {
  const option = page.getByRole('dialog').getByText(label, { exact: true })
  await expect(option).toBeVisible()
  await option.click()
}

/**
 * Submit the New Workspace dialog with its Create button, and wait until the dialog closes and the workspace titled
 * `title` is the active one. The dialog closes when the create RPC returns, and a new workspace activates in place,
 * so no URL changes.
 */
export async function submitNewWorkspaceDialog(page: Page, title: string): Promise<void> {
  const dialog = page.getByRole('dialog')
  await dialog.getByRole('button', { name: 'Create', exact: true }).click()
  await expect(dialog).not.toBeVisible()
  await expect(activeWorkspaceRow(page)).toContainText(title)
}

/**
 * The directory where the Worker places the managed worktree of `branch` for the repository of `repoDir`:
 * `<main repository>-worktrees/<branch>`, beside the main repository.
 *
 * The path is anchored on the MAIN REPO ROOT, matching the worker's placement convention. A path derived from
 * `repoDir` is wrong whenever `repoDir` is itself a linked worktree: the worker still places the new worktree beside
 * the main repository.
 */
export function managedWorktreePath(repoDir: string, branch: string): string {
  const repoRoot = mainRepoRoot(repoDir)
  return join(dirname(repoRoot), `${basename(repoRoot)}-worktrees`, branch)
}

/**
 * Create a workspace on the hub, then open an agent with worktree enabled.
 * Returns the workspace ID and the directory of the new worktree.
 */
export async function createWorkspaceWithWorktreeViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  title: string,
  workingDir: string,
  worktreeBranch: string,
): Promise<{ workspaceId: string, worktreeDir: string }> {
  const workspaceId = await createWorkspaceViaAPI(hubUrl, token, title)
  await openAgentViaAPI(hubUrl, token, workerId, workspaceId, workingDir, {
    createWorktree: true,
    worktreeBranch,
  })

  // OpenAgent returns synchronously with status=STARTING and the worktree is
  // created asynchronously during phased startup (#194). Tests expect
  // `existsSync(worktreeDir)` to be true immediately after this helper returns,
  // so wait until the worker has actually materialized it on disk.
  const worktreeDir = managedWorktreePath(workingDir, worktreeBranch)
  await waitForPathExists(worktreeDir)
  return { workspaceId, worktreeDir }
}

/**
 * The MAIN repository's root for `dir`, even when `dir` is a linked worktree.
 *
 * `--show-toplevel` would answer the worktree's own root, which is exactly the
 * trap this exists to avoid. `--git-common-dir` resolves to the main repo's
 * `.git` from any worktree, so its parent is the main root.
 */
function mainRepoRoot(dir: string): string {
  const commonDir = git(dir, ['rev-parse', '--path-format=absolute', '--git-common-dir']).trim()
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
  const reads: AgentListReads = {}
  while (true) {
    const agents = await listAgentsForWait(hubUrl, token, workerId, workspaceId, reads)
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
        + `(saw ${JSON.stringify(agents)})${lastFailedRead(reads)}`,
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
  const reads: AgentListReads = {}
  while (true) {
    const agents = await listAgentsForWait(hubUrl, token, workerId, workspaceId, reads)
    if (agents.length > 0) {
      return agents
    }
    if (Date.now() >= deadline) {
      throw new Error(`No agents appeared for workspace ${workspaceId} within ${timeoutMs}ms${lastFailedRead(reads)}`)
    }
    await new Promise(r => setTimeout(r, intervalMs))
  }
}

/** The last read of a wait loop over `listAgentsViaAPI` that threw. */
interface AgentListReads {
  lastFailure?: unknown
}

/**
 * Read the agents of the workspace for a wait loop. The Hub read and the channel read throw while the Hub or the Worker
 * restarts, so a read that throws reads as no agent, and `reads` keeps the error for the message of a timeout.
 */
async function listAgentsForWait(hubUrl: string, token: string, workerId: string, workspaceId: string, reads: AgentListReads) {
  try {
    return await listAgentsViaAPI(hubUrl, token, workerId, workspaceId)
  }
  catch (error) {
    reads.lastFailure = error
    return []
  }
}

/** State the last read that threw, as the tail of a timeout message, or '' when no read threw. */
function lastFailedRead(reads: AgentListReads): string {
  if (reads.lastFailure === undefined)
    return ''
  return `; the last read that failed: ${reads.lastFailure instanceof Error ? reads.lastFailure.message : String(reads.lastFailure)}`
}

/**
 * Read the IDs of the workspace's tabs of one type from the hub's ListTabs.
 * The hub's list is the first half of every Worker read below: a Worker RPC takes tab IDs, not a workspace ID.
 */
async function workspaceTabIdsViaAPI(
  hubUrl: string,
  token: string,
  workspaceId: string,
  tabType: 'TAB_TYPE_AGENT' | 'TAB_TYPE_TERMINAL',
): Promise<string[]> {
  const data = await callHub<{ tabs?: Array<{ tabType: string, tabId: string }> }>(
    hubUrl,
    'WorkspaceService/ListTabs',
    { workspaceIds: [workspaceId] },
    { cookie: token, operation: `workspaceTabIdsViaAPI(${workspaceId})` },
  )
  return (data.tabs ?? []).filter(tab => tab.tabType === tabType).map(tab => tab.tabId)
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
  const agentTabIds = await workspaceTabIdsViaAPI(hubUrl, token, workspaceId, 'TAB_TYPE_AGENT')
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
 * The status of one agent of the workspace, as its Worker reports it, or undefined while the Worker lists no such
 * agent. The Worker, not the tab bar, is the authority on an agent's state. The Hub read of the tab list throws when
 * it fails, so a wait on this read uses `waitForAgentStatusViaAPI`, not `expect.poll`.
 */
export async function agentStatusViaAPI(server: AgentServer, workspaceId: string, agentId: string): Promise<number | undefined> {
  const agents = await listAgentsViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId)
  return agents.find(agent => agent.id === agentId)?.status
}

/** Wait until the Worker reports `status` for one agent of the workspace. A read that throws starts the next read. */
export async function waitForAgentStatusViaAPI(server: AgentServer, workspaceId: string, agentId: string, status: AgentStatus): Promise<void> {
  await retryUntilPass(async () => {
    expect(await agentStatusViaAPI(server, workspaceId, agentId), `the Worker reports agent ${agentId} as ${AgentStatus[status]}`).toBe(status)
  })
}

/**
 * Whether the Worker reports one terminal of the workspace as exited, or undefined while it lists no such terminal.
 * A wait on this read uses `waitForTerminalExitViaAPI`, as `agentStatusViaAPI` explains.
 */
export async function terminalExitedViaAPI(server: AgentServer, workspaceId: string, terminalId: string): Promise<boolean | undefined> {
  const terminals = await listTerminalsViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId)
  return terminals.find(terminal => terminal.id === terminalId)?.exited
}

/** Wait until the Worker reports one terminal of the workspace as exited. A read that throws starts the next read. */
export async function waitForTerminalExitViaAPI(server: AgentServer, workspaceId: string, terminalId: string): Promise<void> {
  await retryUntilPass(async () => {
    expect(await terminalExitedViaAPI(server, workspaceId, terminalId), `the Worker reports terminal ${terminalId} as exited`).toBe(true)
  })
}

/**
 * Wait until the workspace holds exactly one agent on its Worker, and return it.
 * More than one agent fails at once, because a test that reads "the" agent would then read an arbitrary one.
 */
export async function waitForSoleAgentViaAPI(
  server: AgentServer,
  workspaceId: string,
): Promise<{ id: string, title: string, workingDir: string, status: number, startupError: string }> {
  const agents = await waitForAgentsViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId)
  const [agent, ...others] = agents
  if (!agent || others.length > 0)
    throw new Error(`Workspace ${workspaceId} must hold exactly one agent, but its Worker lists ${agents.length}: ${agents.map(item => item.id).join(', ')}.`)
  return agent
}

/**
 * Wait until one of the titles that `list` returns equals `title`, and state `message` on a timeout.
 * The Worker's database is the one durable home of a tab title, so `list` reads the Worker, for example through
 * `listTerminalsViaAPI`. A read that throws counts as a miss, and the wait reads again. A timeout reports the last
 * failure: the failed read, or the titles that the last read returned.
 */
export async function waitForWorkerTabTitle(
  list: () => Promise<ReadonlyArray<{ title: string }>>,
  title: string,
  message: string,
): Promise<void> {
  if (title === '')
    throw new Error('A stored tab title check needs a title, because a tab with no title would match an empty one.')
  // `expect.poll` cannot retry a read here: it ends at the first read that throws, and only a failed match starts its
  // next attempt. `toPass` retries the read and the match together.
  await expect(async () => {
    expect((await list()).map(tab => tab.title), message).toContain(title)
  }).toPass({ timeout: waitTimeoutBeforeTestDeadline() })
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
  const terminalTabIds = await workspaceTabIdsViaAPI(hubUrl, token, workspaceId, 'TAB_TYPE_TERMINAL')
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
