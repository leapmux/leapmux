import { expect, test } from './fixtures'
import { dangerToasts } from './helpers/toast'
import { deleteWorkspaceViaUI, loginViaToken, openNewWorkspaceDialog, workspaceRow } from './helpers/ui'
import { withTestWorkspace } from './helpers/workspace'

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
      expect(await dangerToasts(page)).toEqual([])
    })
  }

  test('activates the surviving workspace after deleting the active workspace', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    await withTestWorkspace(leapmuxServer, 'survivor', async (second) => {
      const first = workspaceRow(page, authenticatedEmptyWorkspace.workspaceId)
      const survivor = workspaceRow(page, second.workspaceId)
      await expect(first).toHaveAttribute('data-active', 'true')
      await expect(survivor).toBeVisible()
      await deleteWorkspaceViaUI(page, authenticatedEmptyWorkspace.workspaceId)
      await expect(survivor).toHaveAttribute('data-active', 'true')
    })
  })
})
