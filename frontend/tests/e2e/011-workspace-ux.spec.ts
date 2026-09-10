import { expect, test } from './fixtures'
import { getRecordedToasts } from './helpers/toast'
import { loginViaToken, workspaceRow } from './helpers/ui'
import { withTestWorkspace } from './helpers/workspace'
import { openNewWorkspaceDialog } from './helpers/worktree'

test.describe('workspace navigation', () => {
  test('activates the first workspace on a fresh app load', async ({ page, emptyWorkspace, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    // Do not preselect the workspace. A stored selection would hide a broken initial selection.
    await page.goto('/')
    await expect(workspaceRow(page, emptyWorkspace.workspaceId)).toHaveAttribute('data-active', 'true')
  })

  test('opens the workspace dialog from the section header', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    await openNewWorkspaceDialog(page)
    await page.keyboard.press('Escape')
    await expect(page.getByRole('heading', { name: 'New Workspace' })).toBeHidden()
  })

  for (const { trigger, title } of [
    { trigger: 'empty-tile-open-agent', title: 'New Agent' },
    { trigger: 'new-terminal-button', title: 'New Terminal' },
  ]) {
    test(`opens ${title} from an empty workspace without an error toast`, async ({ page, authenticatedEmptyWorkspace }) => {
      void authenticatedEmptyWorkspace
      await expect(page.getByTestId('tab')).toHaveCount(0)
      await expect(page.getByTestId('empty-tile-actions')).toBeVisible()
      // These buttons have no active tab context, so each must open its directory dialog.
      await page.getByTestId(trigger).click()
      await expect(page.getByRole('heading', { name: title })).toBeVisible()
      await page.keyboard.press('Escape')
      await expect(page.getByRole('heading', { name: title })).toBeHidden()
      expect((await getRecordedToasts(page)).filter(toast => toast.variant === 'danger')).toEqual([])
    })
  }

  test('activates the surviving workspace after deleting the active workspace', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    await withTestWorkspace(leapmuxServer, 'survivor', async (second) => {
      const first = workspaceRow(page, authenticatedEmptyWorkspace.workspaceId)
      const survivor = workspaceRow(page, second.workspaceId)
      await expect(first).toHaveAttribute('data-active', 'true')
      await expect(survivor).toBeVisible()
      await first.getByTestId('workspace-row-menu-trigger').click()
      await first.getByRole('menuitem', { name: 'Delete', exact: true }).click()
      const dialog = page.getByRole('dialog')
      await expect(dialog).toBeVisible()
      await dialog.getByRole('button', { name: 'Delete', exact: true }).click()
      await dialog.getByRole('button', { name: 'Confirm?' }).click()
      await expect(first).toBeHidden()
      await expect(survivor).toHaveAttribute('data-active', 'true')
    })
  })
})
