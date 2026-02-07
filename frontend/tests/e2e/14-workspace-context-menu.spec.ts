import { expect, test } from './fixtures'
import { openWorkspaceContextMenu } from './helpers'

test.describe('Workspace Context Menu', () => {
  test('should show context menu options for owned workspace', async ({ page, authenticatedWorkspace }) => {
    // Open context menu on the workspace item
    const workspaceItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
    const workspaceName = await workspaceItem.textContent()
    await openWorkspaceContextMenu(page, workspaceName!.trim())

    // Should show all options for owner
    await expect(page.getByRole('menuitem', { name: 'Rename' })).toBeVisible()
    await expect(page.getByRole('menuitem', { name: 'Move to' })).toBeVisible()
    await expect(page.getByRole('menuitem', { name: 'Share' })).toBeVisible()
    await expect(page.getByRole('menuitem', { name: 'Delete' })).toBeVisible()
  })

  test('should rename workspace via context menu', async ({ page, authenticatedWorkspace }) => {
    // Open context menu and click Rename
    const workspaceItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
    const workspaceName = await workspaceItem.textContent()
    await openWorkspaceContextMenu(page, workspaceName!.trim())
    await page.getByRole('menuitem', { name: 'Rename' }).click()

    // An inline text input should appear (hasText doesn't match input values, so find input directly)
    const renameInput = workspaceItem.locator('input')
    await expect(renameInput).toBeVisible()
    await expect(renameInput).toBeFocused()

    // Type new name and confirm
    await renameInput.fill('Renamed Workspace')
    await renameInput.press('Enter')

    // Input should disappear and new name should show
    await expect(renameInput).not.toBeVisible()
    await expect(page.getByText('Renamed Workspace')).toBeVisible()
  })

  test('should rename workspace via double-click', async ({ page, authenticatedWorkspace }) => {
    // Double-click the workspace item to start inline rename
    await page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`).dblclick()

    // An inline text input should appear (hasText doesn't match input values, so find input directly)
    const renameInput = page.locator('[data-testid^="workspace-item-"] input')
    await expect(renameInput).toBeVisible()

    // Type new name and confirm
    await renameInput.fill('DblClick Renamed')
    await renameInput.press('Enter')

    // New name should show
    await expect(page.getByText('DblClick Renamed')).toBeVisible()
  })

  test('should cancel rename on Escape', async ({ page, authenticatedWorkspace }) => {
    // Open context menu and click Rename
    const workspaceItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
    const workspaceName = await workspaceItem.textContent()
    await openWorkspaceContextMenu(page, workspaceName!.trim())
    await page.getByRole('menuitem', { name: 'Rename' }).click()

    // Type new name but press Escape (hasText doesn't match input values, so find input directly)
    const renameInput = workspaceItem.locator('input')
    await expect(renameInput).toBeVisible()
    await renameInput.fill('Should Not Save')
    await renameInput.press('Escape')

    // Original name should still be there
    await expect(workspaceItem).toBeVisible()
    await expect(page.getByText('Should Not Save')).not.toBeVisible()
  })

  test('should delete workspace via context menu', async ({ page, authenticatedWorkspace }) => {
    // Get the workspace name from the sidebar
    const workspaceItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
    const workspaceName = await workspaceItem.textContent()

    // Navigate away from the workspace so we're on the dashboard
    await page.goto('/o/admin')
    await expect(page.getByText(workspaceName!.trim())).toBeVisible()

    // Set up dialog handler for the confirm prompt
    page.on('dialog', dialog => dialog.accept())

    // Open context menu and click Delete
    await openWorkspaceContextMenu(page, workspaceName!.trim())
    await page.getByRole('menuitem', { name: 'Delete' }).click()

    // Workspace should be gone
    await expect(page.getByText(workspaceName!.trim())).not.toBeVisible()
  })
})
