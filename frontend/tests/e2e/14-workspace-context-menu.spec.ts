import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI } from './helpers/api'
import { openWorkspaceContextMenu, waitForWorkspaceReady } from './helpers/ui'

test.describe('Workspace Context Menu', () => {
  test('should show context menu options for owned workspace', async ({ page, authenticatedWorkspace }) => {
    // Open context menu on the workspace item
    const workspaceItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
    const workspaceName = await workspaceItem.textContent()
    await openWorkspaceContextMenu(page, workspaceName!.trim())

    // Should show all options for owner
    await expect(page.getByRole('menuitem', { name: 'Rename' })).toBeVisible()
    await expect(page.getByRole('menuitem', { name: 'Share...' })).toBeVisible()
    await expect(page.getByRole('menuitem', { name: 'Archive' })).toBeVisible()
    await expect(page.getByRole('menuitem', { name: 'Delete' })).toBeVisible()
    // "Move to" is hidden when there is only one target section (In Progress)
    await expect(page.getByRole('menuitem', { name: 'Move to' })).not.toBeVisible()
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

  test('should not activate unfocused workspace when opening context menu', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    // Create a second workspace via API (no navigation)
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const secondWorkspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'Unfocused WS', adminOrgId)
    try {
      // Navigate to the SECOND workspace so it becomes active, making the first inactive
      await page.goto(`/o/admin/workspace/${secondWorkspaceId}`)
      await waitForWorkspaceReady(page)

      // The first workspace should appear as inactive in the sidebar
      const firstWorkspaceItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
      await expect(firstWorkspaceItem).toBeVisible()
      await expect(firstWorkspaceItem).not.toHaveClass(/itemActive/)

      // Record URL before opening the context menu
      const urlBefore = page.url()

      // Open context menu on the first (non-active) workspace
      const firstName = await firstWorkspaceItem.textContent()
      await openWorkspaceContextMenu(page, firstName!.trim())

      // Menu should appear
      await expect(page.getByRole('menuitem', { name: 'Rename' })).toBeVisible()

      // URL should not have changed (workspace NOT activated)
      expect(page.url()).toBe(urlBefore)

      // Close the menu
      await page.keyboard.press('Escape')

      // The first workspace should still be inactive
      await expect(firstWorkspaceItem).not.toHaveClass(/itemActive/)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, secondWorkspaceId).catch(() => {})
    }
  })

  test('should not activate unfocused workspace when clicking a menu action', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    // Create a second workspace via API (no navigation)
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const secondWorkspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'Action WS', adminOrgId)
    try {
      // Navigate to the SECOND workspace so it becomes active, making the first inactive
      await page.goto(`/o/admin/workspace/${secondWorkspaceId}`)
      await waitForWorkspaceReady(page)

      const firstWorkspaceItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
      await expect(firstWorkspaceItem).toBeVisible()
      await expect(firstWorkspaceItem).not.toHaveClass(/itemActive/)

      // Record URL before the action
      const urlBefore = page.url()

      // Open context menu and click Archive on the non-active workspace
      const firstName = await firstWorkspaceItem.textContent()
      await openWorkspaceContextMenu(page, firstName!.trim())
      await page.getByRole('menuitem', { name: 'Archive' }).click()

      // Archive confirmation dialog should appear
      const dialog = page.locator('dialog')
      await expect(dialog).toBeVisible()

      // URL should not have changed (workspace NOT activated)
      expect(page.url()).toBe(urlBefore)

      // The first workspace should still be inactive
      await expect(firstWorkspaceItem).not.toHaveClass(/itemActive/)

      // Cancel the archive
      await dialog.getByRole('button', { name: 'Cancel' }).click()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, secondWorkspaceId).catch(() => {})
    }
  })

  test('should delete workspace via context menu', async ({ page, authenticatedWorkspace }) => {
    // Get the workspace name from the sidebar
    const workspaceItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
    const workspaceName = await workspaceItem.textContent()

    // Navigate away from the workspace so we're on the dashboard
    await page.goto('/o/admin')
    await expect(page.getByText(workspaceName!.trim())).toBeVisible()

    // Open context menu and click Delete
    await openWorkspaceContextMenu(page, workspaceName!.trim())
    await page.getByRole('menuitem', { name: 'Delete' }).click()

    // ConfirmDialog should appear
    const dialog = page.locator('dialog')
    await expect(dialog).toBeVisible()
    // Click the danger confirm button (two-step: arm then confirm)
    await dialog.getByRole('button', { name: 'Delete' }).click()
    await dialog.getByRole('button', { name: 'Confirm?' }).click()

    // Workspace should be gone
    await expect(page.getByText(workspaceName!.trim())).not.toBeVisible()
  })
})
