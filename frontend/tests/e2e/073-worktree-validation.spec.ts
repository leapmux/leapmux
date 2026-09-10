import type { Page } from '@playwright/test'
import { execFileSync } from 'node:child_process'
import { expect, test } from './fixtures'
import { loginViaToken, menuOptionTexts } from './helpers/ui'
import { createGitRepo, openNewWorkspaceDialog, setWorkingDir, waitForAppPageReady, waitForWorker } from './helpers/worktree'

// NewWorkspaceDialog and GitOptions unit tests cover validation and mode selection.
// These cases retain the real directory picker, repository discovery, and refresh path.
async function openBranchDialog(page: Page, token: string, repository: string) {
  await loginViaToken(page, token)
  await page.goto('/')
  await waitForAppPageReady(page)
  await openNewWorkspaceDialog(page)
  await waitForWorker(page)
  await setWorkingDir(page, repository)
  await page.getByText('Switch to branch', { exact: true }).click()
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
    await page.getByText('Switch to branch', { exact: true }).click()
    await expect(dialog.getByTestId('branch-select-menu-trigger')).toBeEnabled()
    await expect.poll(async () => {
      const options = await menuOptionTexts(dialog, 'branch-select-menu')
      return { beta: options.includes('beta-branch'), alpha: options.includes('alpha-branch') }
    }).toEqual({ beta: true, alpha: false })
    await dialog.getByRole('button', { name: 'Cancel' }).click()
  })

  test('refresh discovers a branch created while the dialog is open', async ({ page, leapmuxServer }) => {
    const repository = createGitRepo(leapmuxServer.dataDir, 'branches-refresh')
    const dialog = await openBranchDialog(page, leapmuxServer.adminToken, repository)
    expect(await menuOptionTexts(dialog, 'branch-select-menu')).not.toContain('new-after-open')

    execFileSync('git', ['branch', 'new-after-open'], { cwd: repository })
    await dialog.getByLabel('Refresh directory tree').click()
    await expect.poll(() => menuOptionTexts(dialog, 'branch-select-menu')).toContain('new-after-open')
    await dialog.getByRole('button', { name: 'Cancel' }).click()
  })
})
