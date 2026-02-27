import { expect, test } from './fixtures'
import { openWorkspaceContextMenu } from './helpers'

test.describe('Workspace Archive', () => {
  test('should archive workspace via context menu with confirmation dialog', async ({ page, authenticatedWorkspace }) => {
    const workspaceItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
    const workspaceName = await workspaceItem.textContent()

    // Open context menu and click Move to > Archive
    await openWorkspaceContextMenu(page, workspaceName!.trim())
    await page.getByRole('menuitem', { name: 'Move to' }).click()
    await page.getByRole('menuitem', { name: 'Archive' }).click()

    // Confirmation dialog should appear
    const dialog = page.locator('dialog')
    await expect(dialog).toBeVisible()
    await expect(dialog.getByText('Archive Workspace')).toBeVisible()
    await expect(dialog.getByText('All active agents and terminals will be stopped')).toBeVisible()

    // Confirm the archive
    await dialog.getByRole('button', { name: 'Archive' }).click()

    // Workspace should now be in the archived section
    const archivedSection = page.locator('[data-testid="section-header-workspaces_archived"]')
    await expect(archivedSection).toBeVisible()

    // Add button should be hidden and archived empty state shown
    await expect(page.locator('[data-testid="tile-empty-state"]')).toContainText('This workspace is archived')
  })

  test('should cancel archive via confirmation dialog', async ({ page, authenticatedWorkspace }) => {
    const workspaceItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
    const workspaceName = await workspaceItem.textContent()

    // Open context menu and click Move to > Archive
    await openWorkspaceContextMenu(page, workspaceName!.trim())
    await page.getByRole('menuitem', { name: 'Move to' }).click()
    await page.getByRole('menuitem', { name: 'Archive' }).click()

    // Confirmation dialog should appear
    const dialog = page.locator('dialog')
    await expect(dialog).toBeVisible()

    // Cancel the archive
    await dialog.getByRole('button', { name: 'Cancel' }).click()

    // Dialog should close and workspace should still be in its original section
    await expect(dialog).not.toBeVisible()
    await expect(workspaceItem).toBeVisible()
  })

  test('should unarchive workspace and restore normal behavior', async ({ page, authenticatedWorkspace }) => {
    const workspaceItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)

    // Archive the workspace first: open context menu using the workspace item directly
    await workspaceItem.hover()
    await workspaceItem.locator('button').first().click()
    await page.getByRole('menuitem', { name: 'Move to' }).click()
    await page.getByRole('menuitem', { name: 'Archive' }).click()
    await page.locator('dialog').getByRole('button', { name: 'Archive' }).click()

    // Wait for the archived state
    await expect(page.locator('[data-testid="tile-empty-state"]')).toContainText('This workspace is archived')

    // Expand the Archived section (collapsed by default) to reveal the workspace
    await page.locator('[data-testid="section-header-workspaces_archived"]').click()
    await expect(workspaceItem).toBeVisible()

    // Now unarchive it: open context menu using the workspace item directly
    // (cannot use openWorkspaceContextMenu because hasText would include changed menu item text)
    await workspaceItem.hover()
    await workspaceItem.locator('button').first().click()
    await page.getByRole('menuitem', { name: 'Unarchive' }).click()

    // Archived empty state should be gone, replaced by interactive action buttons
    await expect(page.locator('[data-testid="empty-tile-actions"]')).toBeVisible()
  })

  test('should only show In Progress and Custom sections in Move-to menu', async ({ page, authenticatedWorkspace }) => {
    const workspaceItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
    const workspaceName = await workspaceItem.textContent()

    // Open context menu and click Move to to open submenu
    await openWorkspaceContextMenu(page, workspaceName!.trim())
    await page.getByRole('menuitem', { name: 'Move to' }).click()

    // Wait for the submenu to appear
    const archiveButton = page.getByRole('menuitem', { name: 'Archive' })
    await expect(archiveButton).toBeVisible()

    // Shared, Files, To-dos should NOT appear as move targets
    const menuItems = page.getByRole('menuitem')
    const allLabels = await menuItems.allTextContents()
    expect(allLabels).not.toContain('Shared')
    expect(allLabels).not.toContain('Files')
    expect(allLabels).not.toContain('To-dos')
  })

  test('should delete workspace using ConfirmDialog instead of native confirm', async ({ page, authenticatedWorkspace }) => {
    const workspaceItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
    const workspaceName = await workspaceItem.textContent()

    // Navigate away so we can see delete result
    await page.goto('/o/admin')
    await expect(page.getByText(workspaceName!.trim())).toBeVisible()

    // Open context menu and click Delete
    await openWorkspaceContextMenu(page, workspaceName!.trim())
    await page.getByRole('menuitem', { name: 'Delete' }).click()

    // ConfirmDialog should appear (not native dialog)
    const dialog = page.locator('dialog')
    await expect(dialog).toBeVisible()
    await expect(dialog.getByText('Delete Workspace')).toBeVisible()

    // Confirm the delete (need to click twice due to ConfirmButton danger mode)
    await dialog.getByRole('button', { name: 'Delete' }).click() // arms
    await dialog.getByRole('button', { name: 'Confirm?' }).click() // confirms

    // Workspace should be gone
    await expect(page.getByText(workspaceName!.trim())).not.toBeVisible()
  })
})
