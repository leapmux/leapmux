import { execSync } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import {
  branchGroupRow,
  clickBranchMenuItem,
  loginViaToken,
  openWorkspace,
  pickMenuOption,
} from './helpers/ui'
import { createGitRepo } from './helpers/worktree'

test.describe('Repo git focused key alignment', () => {
  test('subdir agent shows git files UI and relabels after branch change', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'focused-key-repo')
    const pkgDir = join(repoDir, 'pkg')
    mkdirSync(pkgDir, { recursive: true })
    writeFileSync(join(pkgDir, 'tracked.txt'), 'hello\n')
    execSync('git add pkg/tracked.txt', { cwd: repoDir })
    execSync('git commit -m "add pkg"', { cwd: repoDir })
    execSync('git branch feature', { cwd: repoDir })
    writeFileSync(join(pkgDir, 'tracked.txt'), 'hello\nchanged\n')

    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Focused Key WS')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, pkgDir)

    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      await expect(page.locator('[data-testid="tree-root-node"]')).toBeVisible()
      await expect(page.locator('[data-testid="files-filter-tab-bar"]')).toBeVisible()
      await expect(branchGroupRow(page)).toContainText('main')

      await page.locator('[data-testid="files-filter-changed"]').click()
      await expect(page.locator('[data-testid="git-diff-stats"]').first()).toBeVisible()

      await clickBranchMenuItem(page, branchGroupRow(page), 'Change branch...')
      const dialog = page.getByRole('dialog')
      await expect(dialog.getByRole('heading', { name: 'Change branch' })).toBeVisible()
      await dialog.getByText('Switch to branch').click()
      await pickMenuOption(dialog, 'branch-select-menu', 'feature')
      await dialog.getByRole('button', { name: 'Apply' }).click()
      await expect(dialog.getByRole('heading', { name: 'Change branch' })).not.toBeVisible()

      await expect(branchGroupRow(page)).toContainText('feature')
      await expect(branchGroupRow(page)).not.toContainText('main')
      await expect(page.locator('[data-testid="files-filter-tab-bar"]')).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
