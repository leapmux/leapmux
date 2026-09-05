import type { Page } from '@playwright/test'
import { execSync } from 'node:child_process'
import { expect, test } from './fixtures'
import { clickRepoMenuItem, loginViaToken, openRepoMenu, openWorkspace, repoGroupRow, repoMenuItem } from './helpers/ui'
import { createGitRepo, createWorkspaceWithWorktreeViaAPI } from './helpers/worktree'

/**
 * The repository row's menu.
 *
 * Its item set and its two shapes -- flat for one checkout, one submenu per
 * checkout for several -- are unit-tested in
 * `src/components/workspace/RepoContextMenu.test.tsx`. What needs a real
 * browser here is the half vitest stubs: the kebab that only paints on hover,
 * the right-click gesture, a `popover` that really opens, and
 * `Collapse all branches` reaching the rows it names.
 *
 * A browser session is not a desktop-solo one, so no Worker is local and the
 * three local-only items -- `Reveal in file manager`, `Open in <app>` and the
 * `Open in…` submenu -- are hidden by design. This asserts the structure a
 * browser really shows.
 *
 * Every lookup goes through `repoMenuItem`, which is scoped to ONE row and
 * matches the name exactly. Both halves are required: `DropdownMenu` renders
 * its children eagerly, so every repository row on screen holds a hidden copy
 * of each item, and Playwright matches an accessible name by substring unless
 * told otherwise -- which would let `Copy repository URL` answer for
 * `Copy repository path`.
 */

/** A repository with an `origin`, so the menu's URL row has something to copy. */
const ORIGIN_URL = 'https://example.com/acme/repo-row-menu.git'

function createRepoWithOrigin(dataDir: string, name: string): string {
  const repoDir = createGitRepo(dataDir, name)
  execSync(`git remote add origin ${ORIGIN_URL}`, { cwd: repoDir })
  return repoDir
}

/** This workspace's own subtree, so another spec's expanded rows stay out. */
function workspaceSubtree(page: Page, workspaceId: string) {
  return page.locator(`[data-testid="workspace-children-${workspaceId}"]`)
}

test.describe('repository row menu', () => {
  test('the kebab and a right-click both open it, with the repository block and the collapse item', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createRepoWithOrigin(dataDir, 'repo-menu-repo')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Repo Menu WS',
      repoDir,
      'repo-menu-branch',
    )

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    const repoRow = repoGroupRow(workspaceSubtree(page, workspaceId))
    await expect(repoRow).toBeVisible()

    // ── The kebab ───────────────────────────────────────────────────────────
    await openRepoMenu(repoRow)

    // One checkout, so the actions render flat -- a submenu holding the only
    // choice is a click nobody should have to make.
    const menu = page.locator('menu[popover]:visible')
    await expect(menu.getByText('Checkouts', { exact: true })).toHaveCount(0)

    // The same three sections the branch row carries, in the same order.
    await expect(menu.getByText('Agents', { exact: true })).toBeVisible()
    await expect(menu.getByText('Terminals', { exact: true })).toBeVisible()
    await expect(menu.getByText('Repository', { exact: true })).toBeVisible()

    await expect(repoMenuItem(repoRow, 'Copy repository URL')).toBeVisible()
    await expect(repoMenuItem(repoRow, 'Copy repository path')).toBeVisible()
    await expect(repoMenuItem(repoRow, 'Collapse all branches')).toBeVisible()

    // Local-only, and no Worker is local in a browser session.
    await expect(repoMenuItem(repoRow, 'Reveal in file manager')).toHaveCount(0)
    await expect(repoMenuItem(repoRow, 'Open in…')).toHaveCount(0)

    // Close it, so the right-click below opens rather than dismisses.
    await page.keyboard.press('Escape')
    await expect(menu).toHaveCount(0)

    // ── The right-click ─────────────────────────────────────────────────────
    const box = (await repoRow.boundingBox())!
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
    await page.mouse.down({ button: 'right' })
    await page.mouse.up({ button: 'right' })

    await expect(repoMenuItem(repoRow, 'Collapse all branches')).toBeVisible()
    // Still up a moment later: the menu opens after the release, so the
    // release's own light-dismiss pass cannot take it back down.
    await expect(repoMenuItem(repoRow, 'Copy repository path')).toBeVisible()
  })

  // `Collapse all branches` is distinct from the row's own click, which folds
  // the repository group itself. This one folds the branch rows inside it, and
  // goes dim once there is nothing left to fold.
  test('collapse all branches folds the branch rows and then goes dim', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createRepoWithOrigin(dataDir, 'repo-collapse-repo')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Repo Collapse WS',
      repoDir,
      'repo-collapse-branch',
    )

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    const subtree = workspaceSubtree(page, workspaceId)
    const leaves = subtree.locator('[data-testid="tab-tree-leaf"]:visible')
    await expect(leaves.first()).toBeVisible()

    const repoRow = repoGroupRow(subtree)
    await clickRepoMenuItem(repoRow, 'Collapse all branches')

    // The branch row survives -- it is the repository group that stays open --
    // but the tabs under it are folded away.
    await expect(subtree.locator('[data-testid="tab-tree-branch-group"]:visible')).toHaveCount(1)
    await expect(leaves).toHaveCount(0)

    // Nothing left to fold, so the item states that instead of doing nothing.
    await openRepoMenu(repoRow)
    await expect(repoMenuItem(repoRow, 'Collapse all branches')).toBeDisabled()
  })
})
