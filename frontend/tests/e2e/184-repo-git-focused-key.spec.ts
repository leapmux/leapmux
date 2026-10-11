import { execFileSync } from 'node:child_process'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import {
  branchGroupRow,
  clickBranchMenuItem,
  pickMenuOption,
} from './helpers/ui'
import { showWorkspaceWithAgents } from './helpers/workspace'
import { commitFile, createGitRepo } from './helpers/worktree'

test.describe('Repo git focused key alignment', () => {
  test('subdir agent shows git files UI and relabels after branch change', async ({
    page,
    leapmuxServer,
  }) => {
    const repoDir = createGitRepo(leapmuxServer.dataDir, 'focused-key-repo')
    const pkgDir = join(repoDir, 'pkg')
    commitFile(repoDir, 'pkg/tracked.txt', 'hello\n', 'add pkg')
    execFileSync('git', ['branch', 'feature'], { cwd: repoDir })
    writeFileSync(join(pkgDir, 'tracked.txt'), 'hello\nchanged\n')

    await showWorkspaceWithAgents(page, leapmuxServer, 'Focused Key WS', { workingDir: pkgDir })

    await expect(page.locator('[data-testid="tree-root-node"]')).toBeVisible()
    await expect(page.locator('[data-testid="files-filter-tab-bar"]')).toBeVisible()
    await expect(branchGroupRow(page)).toContainText('main')

    await page.locator('[data-testid="files-filter-changed"]').click()
    await expect(page.locator('[data-testid="git-diff-stats"]').first()).toBeVisible()

    await clickBranchMenuItem(page, branchGroupRow(page), 'Switch to branch...')
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading', { name: 'Change branch' })).toBeVisible()
    await dialog.getByText('Switch to branch', { exact: true }).click()
    await pickMenuOption(dialog, 'branch-select-menu', 'feature')
    await dialog.getByRole('button', { name: 'Apply' }).click()
    await expect(dialog.getByRole('heading', { name: 'Change branch' })).not.toBeVisible()

    await expect(branchGroupRow(page)).toContainText('feature')
    await expect(branchGroupRow(page)).not.toContainText('main')
    await expect(page.locator('[data-testid="files-filter-tab-bar"]')).toBeVisible()
  })
})
