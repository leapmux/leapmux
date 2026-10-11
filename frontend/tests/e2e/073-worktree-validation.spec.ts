import type { Page } from '@playwright/test'
import { execFileSync } from 'node:child_process'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { retryUntilPass } from './helpers/retryUntilPass'
import { chooseGitMode, menuOptionTexts, openNewWorkspaceDialogAt, setWorkingDir } from './helpers/ui'
import { createGitRepo } from './helpers/worktree'

// NewWorkspaceDialog and GitOptions unit tests cover validation and mode selection.
// These cases retain the real directory picker, repository discovery, and refresh path.
async function openBranchDialog(page: Page, token: string, repository: string) {
  await openNewWorkspaceDialogAt(page, token, repository)
  await chooseGitMode(page, 'Switch to branch')
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByTestId('branch-select-menu-trigger')).toBeEnabled()
  return dialog
}

test.describe('repository branch discovery', () => {
  test('replaces the branch list and resets the mode when the repository changes', async ({ page, leapmuxServer }) => {
    const { adminToken, dataDir } = leapmuxServer
    const first = createGitRepo(dataDir, 'branches-first')
    const second = createGitRepo(dataDir, 'branches-second')
    execFileSync('git', ['branch', 'alpha-branch'], { cwd: first })
    execFileSync('git', ['branch', 'beta-branch'], { cwd: second })

    const dialog = await openBranchDialog(page, adminToken, first)
    const initial = await menuOptionTexts(dialog, 'branch-select-menu')
    expect(initial).toContain('alpha-branch')
    expect(initial).not.toContain('beta-branch')

    await setWorkingDir(page, second)
    await expect(page.getByLabel('Use current state')).toBeChecked()
    await chooseGitMode(page, 'Switch to branch')
    await expect(dialog.getByTestId('branch-select-menu-trigger')).toBeEnabled()
    // Each read clicks the menu trigger, which `LoadingMenu` disables while it loads. A click that times out on the
    // disabled trigger throws, so the wait retries the read.
    await retryUntilPass(async () => {
      const options = await menuOptionTexts(dialog, 'branch-select-menu')
      expect({ beta: options.includes('beta-branch'), alpha: options.includes('alpha-branch') }, 'the menu lists the branches of the new directory')
        .toEqual({ beta: true, alpha: false })
    })
    await dialog.getByRole('button', { name: 'Cancel' }).click()
  })

  test('refresh discovers a branch created while the dialog is open', async ({ page, leapmuxServer }) => {
    const repository = createGitRepo(leapmuxServer.dataDir, 'branches-refresh')
    const dialog = await openBranchDialog(page, leapmuxServer.adminToken, repository)
    expect(await menuOptionTexts(dialog, 'branch-select-menu')).not.toContain('new-after-open')

    execFileSync('git', ['branch', 'new-after-open'], { cwd: repository })
    await dialog.getByLabel('Refresh directory tree').click()
    await retryUntilPass(async () => {
      expect(await menuOptionTexts(dialog, 'branch-select-menu'), 'the refreshed menu lists the new branch').toContain('new-after-open')
    })
    await dialog.getByRole('button', { name: 'Cancel' }).click()
  })
})
