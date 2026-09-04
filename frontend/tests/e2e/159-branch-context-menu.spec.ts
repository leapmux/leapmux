import type { Page } from '@playwright/test'
import { execSync } from 'node:child_process'
import { existsSync, realpathSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { getRecordedToasts } from './helpers/toast'
import { branchGroupRow, clickBranchMenuItem, loginViaToken, openBranchMenu, openWorkspace, pickMenuOption, workspaceRow } from './helpers/ui'
import {
  branchExists,
  createGitRepo,
  createWorkspaceWithWorktreeViaAPI,
  expectRepoBranch,
  listAgentsViaAPI,
  waitForAgentStartupViaAPI,
} from './helpers/worktree'

/**
 * ConfirmButton arms on the first click and fires on the second.
 *
 * The button is named after what it destroys -- "Delete worktree" removes a
 * directory, "Delete branch" does not -- so `kind` picks the spelling. Every
 * caller states it, because a helper that matched either one would let a
 * worktree dialog pass a test written for a branch.
 */
async function confirmDelete(page: Page, kind: 'branch' | 'worktree') {
  await page.getByRole('button', { name: `Delete ${kind}` }).click()
  await page.getByRole('button', { name: 'Confirm?' }).click()
}

test.describe('Branch context menu', () => {
  test('three-dot menu opens with the git items, the delete item and the new-tab sections', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'branch-menu-repo')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Branch Menu WS',
      repoDir,
      'menu-branch',
    )

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    const branchRow = branchGroupRow(page)
    await expect(branchRow).toBeVisible()
    await openBranchMenu(page, branchRow)

    // One item per git mode, so the mode is visible before the dialog opens.
    for (const name of ['Switch to branch...', 'Create new branch...', 'Create new worktree...'])
      await expect(page.getByRole('menuitem', { name })).toBeVisible()
    await expect(page.getByRole('menuitem', { name: 'Change branch...' })).toHaveCount(0)

    // A worktree workspace, so the delete item states the worktree. The change
    // items keep their names: a worktree has a branch checked out either way.
    await expect(page.getByRole('menuitem', { name: 'Delete worktree...' })).toBeVisible()
    await expect(page.getByRole('menuitem', { name: 'Delete branch...' })).toHaveCount(0)

    // The Agents / Terminals block, listing the BRANCH worker's own shells.
    const menu = page.locator('menu[popover]:visible')
    await expect(menu.getByText('Agents', { exact: true })).toBeVisible()
    await expect(menu.getByText('Terminals', { exact: true })).toBeVisible()
    await expect(page.getByRole('menuitem', { name: 'New agent...' })).toBeVisible()
    await expect(page.getByRole('menuitem', { name: 'New terminal...' })).toBeVisible()
    await expect(menu.getByRole('menuitem', { name: /\/bin\// }).first()).toBeVisible()
  })

  // Every item of this menu either changes branch state or opens a tab, and an
  // archived workspace refuses both — so there is nothing left to dim and the
  // row carries no menu at all.
  test('an archived workspace\'s branch row carries no menu', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'branch-archived-repo')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Branch Archived WS',
      repoDir,
      'archived-branch',
    )

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    // Scoped to THIS workspace's subtree, not `branchGroupRow`: that one takes
    // the first visible row in the whole sidebar, and every workspace an
    // earlier test expanded still has one. Archiving moves this workspace to
    // another section, so the unscoped locator drifted onto a live workspace's
    // row and read its menu as this row's.
    // `:visible` as well, because a collapsed sidebar keeps its rows mounted
    // under `display: none`.
    const row = page
      .locator(`[data-testid="workspace-children-${workspaceId}"] [data-testid="tab-tree-branch-group"]:visible`)
      .first()

    // The menu is there while the workspace is live, so its absence below is
    // the archive doing it rather than the row never having had one.
    await expect(row).toContainText('archived-branch')
    await expect(row.locator('[aria-expanded]')).toHaveCount(1)

    const wsRow = workspaceRow(page, workspaceId)
    await wsRow.hover()
    await wsRow.locator('button').first().click()
    // `exact`: this is the WORKSPACE row's menu, which now also offers
    // "New agent in archived-branch..." -- and Playwright matches an
    // accessible name by substring unless told otherwise.
    await page.getByRole('menuitem', { name: 'Archive', exact: true }).click()
    await page.locator('dialog').getByRole('button', { name: 'Archive' }).click()
    await expect(page.locator('[data-testid="section-header-workspaces_archived"]')).toBeVisible()

    // The row itself survives — it still groups the tabs and carries the diff
    // badge — but its kebab is gone, and with it every item.
    await expect(row).toBeVisible()
    await expect(row).toContainText('archived-branch')
    await expect(row.locator('[aria-expanded]')).toHaveCount(0)
  })

  // The three items differ only in the radio the dialog opens on, and that is
  // the whole point of splitting them: the mode used to be invisible until the
  // dialog was already open on "Switch to branch".
  test('each change item opens the dialog on its own mode', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'branch-mode-repo')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Branch Mode WS',
      repoDir,
      'mode-branch',
    )

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    const dialog = page.getByRole('dialog')
    for (const [item, mode] of [
      ['Switch to branch...', 'Switch to branch'],
      ['Create new branch...', 'Create new branch'],
      ['Create new worktree...', 'Create new worktree'],
    ]) {
      await clickBranchMenuItem(page, branchGroupRow(page), item)
      await expect(dialog.getByRole('heading', { name: 'Change branch' })).toBeVisible()
      await expect(dialog.getByRole('radio', { name: mode })).toBeChecked()
      await dialog.getByRole('button', { name: 'Cancel' }).click()
      await expect(dialog.getByRole('heading', { name: 'Change branch' })).not.toBeVisible()
      // The menu STAYS OPEN behind the dialog -- the modal takes the click, so
      // the popover's light dismiss never fires -- and it closes only once the
      // dialog is gone. Wait for that: `openBranchMenu` skips its trigger when
      // the item still reads visible, so the next round would click an item
      // that vanishes under it and wait out the whole action timeout.
      await expect(page.locator('menu[popover]:visible')).toHaveCount(0)
    }
  })

  // The branch row states a (worker, working directory) pair that a new agent
  // or terminal almost always wants. Before this, the only way to start one
  // there was the tab bar's menu, which acts on the CURRENT tab instead.
  //
  // What this covers is the WIRING, end to end: a shell item reaches the
  // worker's OpenTerminal and the tab lands under the branch it was opened
  // from. Which directory each surface chooses is pinned by the unit tests --
  // both surfaces resolve to the same one in a single-repo workspace, so an
  // E2E here could not tell them apart.
  test('opens a terminal from the menu\'s shell item', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'branch-newtab-repo')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Branch New Tab WS',
      repoDir,
      'newtab-branch',
    )

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    const terminalTabs = page.locator('[data-testid="tab"][data-tab-type="terminal"]')
    const before = await terminalTabs.count()
    await openBranchMenu(page, branchGroupRow(page))
    await page.locator('menu[popover]:visible').getByRole('menuitem', { name: /\/bin\// }).first().click()

    await expect(terminalTabs).toHaveCount(before + 1)
    // Under the branch row it was opened from, not in the ungrouped bucket.
    await expect(branchGroupRow(page)).toContainText('newtab-branch')
  })

  // The glyph row's counterpart to the shell-item test above, and the same
  // scope: it pins the WIRING, not the directory. A provider glyph states the
  // branch worker's own provider list, which is the half a mount-time fetch
  // would have taken from the active tab's worker instead.
  test('opens an agent from the menu\'s provider glyph', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'branch-glyph-repo')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Branch Glyph WS',
      repoDir,
      'glyph-branch',
    )

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    const before = await agentTabs.count()
    await openBranchMenu(page, branchGroupRow(page))
    // The first provider the branch's Worker reports. Which one it is depends
    // on what the test machine has installed, so the test states none.
    await page.locator('menu[popover]:visible [data-testid^="menu-new-agent-"]').first().click()

    await expect(agentTabs).toHaveCount(before + 1)
    // Under the branch row it was opened from, not in the ungrouped bucket.
    await expect(branchGroupRow(page)).toContainText('glyph-branch')
  })

  // Decision 1 of the design, and the half no unit test can reach: the sidebar
  // lists EVERY workspace's tree, so a branch row often belongs to a workspace
  // the user is not looking at. The new tab is placed on the ACTIVE workspace's
  // focused tile, so the row's own workspace has to become active first -- and
  // in the same tick, because the placement follows the switch synchronously.
  // Without it the pty is created on the Worker and no tab ever points at it.
  //
  // Both workspaces sit on the one Worker the fixture starts, so this pins the
  // WORKSPACE axis only. The Worker axis is the branch row's own `workerId`,
  // which the unit tests pin per row.
  test('a new tab from another workspace\'s branch row switches to that workspace', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const homeRepo = createGitRepo(dataDir, 'branch-home-repo')
    const awayRepo = createGitRepo(dataDir, 'branch-away-repo')

    const homeWs = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Branch Home WS',
      homeRepo,
      'home-branch',
    )
    const awayWs = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Branch Away WS',
      awayRepo,
      'away-branch',
    )

    await loginViaToken(page, adminToken)
    // AWAY first, so its tree is hydrated and its branch row is on screen while
    // HOME is the active one. A workspace the session never activated may still
    // be waiting on the git status its branch grouping is derived from, and the
    // row this test clicks would not exist yet.
    await openWorkspace(page, awayWs)
    await openWorkspace(page, homeWs)
    await expect(workspaceRow(page, homeWs)).toHaveAttribute('data-active', 'true')
    await expect(workspaceRow(page, awayWs)).toHaveAttribute('data-active', 'false')

    // Scoped to the AWAY subtree: `branchGroupRow` takes the first visible row
    // in the whole sidebar, which is the active workspace's. `:visible` as well,
    // because a collapsed sidebar keeps its rows mounted under
    // `display: none`.
    const awayBranchRow = page
      .locator(`[data-testid="workspace-children-${awayWs}"] [data-testid="tab-tree-branch-group"]:visible`)
      .first()
    await expect(awayBranchRow).toContainText('away-branch')

    const terminalTabs = page.locator('[data-testid="tab"][data-tab-type="terminal"]')
    const before = await terminalTabs.count()
    await openBranchMenu(page, awayBranchRow)
    await page.locator('menu[popover]:visible').getByRole('menuitem', { name: /\/bin\// }).first().click()

    // The switch, then the tab -- in that order, which is the whole point.
    await expect(workspaceRow(page, awayWs)).toHaveAttribute('data-active', 'true')
    await expect(workspaceRow(page, homeWs)).toHaveAttribute('data-active', 'false')
    await expect(terminalTabs).toHaveCount(before + 1)
    // Filed under the branch it was opened from, in the workspace that owns it.
    await expect(
      page.locator(`[data-testid="workspace-children-${awayWs}"] [data-testid="tab-tree-branch-group"]:visible`).first(),
    ).toContainText('away-branch')
  })

  test('opens the New agent dialog from the menu', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'branch-prefill-repo')
    const realRepoDir = realpathSync(repoDir)

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Branch Prefill WS',
      repoDir,
      'prefill-branch',
    )

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    await clickBranchMenuItem(page, branchGroupRow(page), 'New agent...')

    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading', { name: 'New Agent' })).toBeVisible()
    // The repository the branch row stated. `NewAgentDialog` remaps a worktree
    // root to the canonical repo root for its git options, exactly as it does
    // from the tab bar (see 072), so this is the repo root either way.
    await expect(dialog.getByPlaceholder('Enter path...')).toHaveValue(realRepoDir)

    await dialog.getByRole('button', { name: 'Cancel' }).click()
  })

  test('delete branch (worktree variant) removes worktree and branch', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'branch-delete-repo')
    const realDataDir = realpathSync(dataDir)
    const worktreeDir = join(realDataDir, 'branch-delete-repo-worktrees', 'delete-branch')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Branch Delete WS',
      repoDir,
      'delete-branch',
    )
    expect(existsSync(worktreeDir)).toBe(true)
    // `createWorkspaceWithWorktreeViaAPI` waits only for the worktree
    // DIRECTORY. The worker writes the `worktree_tabs` row later, on the same
    // async startup goroutine, and the REMOVE below reclaims the worktree only
    // when the last row that references it goes. `waitForAgentStartupViaAPI`
    // is what that helper's own comment prescribes for a test that closes a
    // tab and expects a worktree effect.
    await waitForAgentStartupViaAPI(hubUrl, adminToken, workerId, workspaceId)

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    await clickBranchMenuItem(page, branchGroupRow(page), 'Delete worktree...')

    await expect(page.getByRole('heading', { name: 'Delete worktree' })).toBeVisible()

    // Worktree variant: dialog shows the worktree path.
    await expect(page.getByRole('dialog').getByText(/branch-delete-repo-worktrees\/delete-branch/)).toBeVisible()

    await confirmDelete(page, 'worktree')

    // The dialog holds open for its removal PREFLIGHT only. Then it hands
    // the coupled tab closes off and dismisses. It does not wait for the
    // removal, because that stops the agent and deletes the directory, which
    // takes seconds and asks the user nothing.
    await expect(page.getByRole('heading', { name: 'Delete worktree' })).not.toBeVisible()

    // Removal is coupled to the tab closes (WorktreeAction.REMOVE), so the
    // worker tears down the worktree dir + branch in the background once
    // the last referencing tab closes; failures surface via toast.
    await expect(async () => {
      expect(existsSync(worktreeDir)).toBe(false)
      expect(branchExists(repoDir, 'delete-branch')).toBe(false)
    }).toPass()
  })

  test('delete branch (worktree variant) refuses a locked worktree before the user can arm Delete', async ({
    page,
    leapmuxServer,
  }) => {
    // The preflight is the cost of the dialog closing early. `git worktree
    // lock` is the refusal that the single `--force` behind the removal does
    // NOT override. Without the preflight this close destroys the tab,
    // dismisses the dialog, and only then finds that git refuses -- with no
    // surface left to say so.
    //
    // The verdict rides on the OPEN-time inspect, so the refusal is stated
    // before the user arms a destructive two-click confirm rather than after
    // firing it.
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'branch-delete-locked-repo')
    const realDataDir = realpathSync(dataDir)
    const worktreeDir = join(realDataDir, 'branch-delete-locked-repo-worktrees', 'locked-branch')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Branch Delete Locked WS',
      repoDir,
      'locked-branch',
    )
    expect(existsSync(worktreeDir)).toBe(true)
    await waitForAgentStartupViaAPI(hubUrl, adminToken, workerId, workspaceId)
    execSync(`git worktree lock --reason "held by the e2e test" "${worktreeDir}"`, { cwd: repoDir })

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    await clickBranchMenuItem(page, branchGroupRow(page), 'Delete worktree...')
    await expect(page.getByRole('heading', { name: 'Delete worktree' })).toBeVisible()

    // Delete is unavailable, and git's own reason says why. Visible text, not
    // the button's tooltip alone: a greyed-out destructive option with no
    // stated reason looks like a defect.
    //
    // By TEST ID, not by text. The same reason reaches the button's <Tooltip>,
    // which renders it a second time as an offscreen aria-describedby node, so
    // a text match resolves to two elements and dies on strict mode.
    await expect(
      page.getByRole('dialog').getByTestId('branch-delete-blocked-reason'),
    ).toHaveText(/held by the e2e test/)
    // By role+name, which is what the button carries again now that no control
    // here takes a `title`. It used to be located by TEXT, because a `title`
    // long enough to state a reason BECAME the accessible name and stopped
    // `{ name: 'Delete worktree' }` from matching in exactly this state.
    await expect(page.getByRole('button', { name: 'Delete worktree' })).toBeDisabled()

    // The escape hatch is offered instead, so the dialog is not a dead end.
    await expect(page.getByRole('button', { name: 'Close tabs, keep worktree' })).toBeEnabled()

    // "Destroys nothing" needs a settle, not a first observation. A
    // regression that fires the closes beside the refusal tombstones the tab
    // at once, and the removal behind it then takes seconds -- so an
    // immediate existsSync and a `toBeVisible` that passes on the first frame
    // both stay green while the group is torn down. Poll the WORKER's own
    // agent list, which no optimistic client state can answer for, and hold
    // it: the agent must still be running after the dialog refused.
    await expect(async () => {
      const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId)
      expect(agents).toHaveLength(1)
    }).toPass()
    expect(existsSync(worktreeDir)).toBe(true)
    expect(branchExists(repoDir, 'locked-branch')).toBe(true)
    // The tab is still there to return to, which is why the refusal has to
    // come before the close rather than after it.
    await expect(branchGroupRow(page)).toBeVisible()
  })

  test('delete branch (worktree variant) closes the tabs and keeps a locked worktree', async ({
    page,
    leapmuxServer,
  }) => {
    // The escape hatch, end to end. A refused removal must not strand the user
    // with Cancel as the dialog's one action: the tabs are still closable, and
    // only the removal is refused. This is the counterpart of the last-tab
    // dialog's "Close anyway".
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'branch-keep-locked-repo')
    const realDataDir = realpathSync(dataDir)
    const worktreeDir = join(realDataDir, 'branch-keep-locked-repo-worktrees', 'keep-locked-branch')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Branch Keep Locked WS',
      repoDir,
      'keep-locked-branch',
    )
    await waitForAgentStartupViaAPI(hubUrl, adminToken, workerId, workspaceId)
    execSync(`git worktree lock --reason "held by the e2e test" "${worktreeDir}"`, { cwd: repoDir })

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    await clickBranchMenuItem(page, branchGroupRow(page), 'Delete worktree...')
    await expect(page.getByRole('heading', { name: 'Delete worktree' })).toBeVisible()
    // ConfirmButton arms on the first click and fires on the second.
    await page.getByRole('button', { name: 'Close tabs, keep worktree' }).click()
    await page.getByRole('button', { name: 'Confirm?' }).click()

    await expect(page.getByRole('heading', { name: 'Delete worktree' })).not.toBeVisible()

    // The tabs go, and the worktree stays — which is the whole point of the
    // hatch. Poll the worker for the tab count; the directory check follows it,
    // because the removal (if a regression let one run) takes seconds.
    await expect(async () => {
      const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId)
      expect(agents).toHaveLength(0)
    }).toPass()
    expect(existsSync(worktreeDir)).toBe(true)
    expect(branchExists(repoDir, 'keep-locked-branch')).toBe(true)
  })

  test('delete branch (in-place variant) relabels the sidebar without a reload', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'branch-delete-inplace-repo')
    // A plain repo checked out on the doomed branch -- no worktree. That is
    // what puts the dialog on its non-worktree path. The user picks a
    // switch-to branch, and the worker then runs checkout and `git branch
    // -D` in place. Every tab stays on the new branch.
    // `createGitRepo` already persists `core.fsmonitor false` into this
    // repo's config, so this command needs no `-c` override.
    execSync('git checkout -b doomed-branch', { cwd: repoDir })

    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Branch Delete In Place WS')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, repoDir)
    // Wait for the agent to leave STARTING before the browser subscribes.
    // `activeTabReady` keys AppShell's git-status effect, so a STARTING to
    // ACTIVE flip that lands AFTER the delete re-runs that effect and
    // relabels the sidebar on its own -- which would let this test pass
    // against the broken code. The ACTIVE broadcast also carries the git
    // snapshot taken at phase 1, so a late one reverts the label to
    // `doomed-branch` and would fail the test against the FIXED code. One
    // wait closes both directions.
    await waitForAgentStartupViaAPI(hubUrl, adminToken, workerId, workspaceId)

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    await expect(branchGroupRow(page)).toContainText('doomed-branch')

    await clickBranchMenuItem(page, branchGroupRow(page), 'Delete branch...')
    await expect(page.getByRole('heading', { name: 'Delete branch' })).toBeVisible()

    const dialog = page.getByRole('dialog')
    await expect(dialog.getByText('Switch this working directory to:')).toBeVisible()
    await pickMenuOption(dialog, 'branch-select-menu', 'main')

    await confirmDelete(page, 'branch')
    await expect(page.getByRole('heading', { name: 'Delete branch' })).not.toBeVisible()

    // The worker did the work.
    await expectRepoBranch(repoDir, 'main')
    expect(branchExists(repoDir, 'doomed-branch')).toBe(false)

    // The regression this pins: the sidebar kept the DELETED branch's label
    // (plus a warn toast) until the user reloaded the page. The dialog closed
    // before it notified the shell. The callback then read the <Show> state
    // that the close disposed, so the branch stamp and the git-status
    // refresh never ran.
    await expect(branchGroupRow(page)).toContainText('main')
    await expect(branchGroupRow(page)).not.toContainText('doomed-branch')

    // The broken path caught the throw and warned instead. The absence of
    // that warning is the race-free half of this assertion, because the
    // dialog raises it before `onClose`, which the wait above already
    // covers.
    const toasts = await getRecordedToasts(page)
    expect(toasts.map(t => t.message)).not.toContain(
      'Branch deleted, but failed to update the sidebar label',
    )
  })

  test('change branch dialog offers all three git options and cancels cleanly', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'branch-change-repo')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Branch Change WS',
      repoDir,
      'change-branch',
    )

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    await clickBranchMenuItem(page, branchGroupRow(page), 'Switch to branch...')

    await expect(page.getByRole('heading', { name: 'Change branch' })).toBeVisible()
    // Whichever item opened it, the dialog still offers all three modes: the
    // menu item preselects a radio, it does not narrow the dialog. Which radio
    // each item selects is pinned by the mode test above.
    await expect(page.getByRole('dialog').getByText('Switch to branch', { exact: true })).toBeVisible()
    await expect(page.getByRole('dialog').getByText('Create new branch', { exact: true })).toBeVisible()
    await expect(page.getByRole('dialog').getByText('Create new worktree', { exact: true })).toBeVisible()

    // Cancel closes the dialog without changes.
    await page.getByRole('button', { name: 'Cancel' }).click()
    await expect(page.getByRole('heading', { name: 'Change branch' })).not.toBeVisible()
  })
})
