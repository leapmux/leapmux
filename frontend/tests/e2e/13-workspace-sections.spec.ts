import { expect, test } from './fixtures'
import { openWorkspaceContextMenu } from './helpers'

test.describe('Workspace Sections', () => {
  test('should show default sections on first load', async ({ page, authenticatedWorkspace }) => {
    // "In Progress" section should be visible as the default section
    await expect(page.locator('[data-testid="section-header-in_progress"]')).toBeVisible()
  })

  test('should create workspace in the In Progress section', async ({ page, authenticatedWorkspace }) => {
    // Workspace should appear in the sidebar (fixture auto-creates workspace)
    await expect(page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)).toBeVisible()
  })

  test('should toggle section collapse', async ({ page, authenticatedWorkspace }) => {
    // Workspace should be visible initially
    const wsItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
    await expect(wsItem).toBeVisible()

    // Click the section header summary to collapse it
    await page.locator('[data-testid="section-header-in_progress"] > summary').click()

    // Workspace should be hidden
    await expect(wsItem).not.toBeVisible()

    // Click the section header summary again to expand
    await page.locator('[data-testid="section-header-in_progress"] > summary').click()

    // Workspace should be visible again
    await expect(wsItem).toBeVisible()
  })

  test('should move workspace to archived section via context menu', async ({ page, authenticatedWorkspace }) => {
    // Open context menu on our workspace (find it by the specific workspace-item testid)
    const wsItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
    const workspaceName = await wsItem.textContent()
    await openWorkspaceContextMenu(page, workspaceName!.trim())

    // Click "Move to" submenu trigger to open the sub-menu (uses popovertarget)
    await page.getByRole('menuitem', { name: 'Move to' }).click()

    // Click "Archive"
    await page.getByRole('menuitem', { name: 'Archive' }).click()

    // Archived section header should appear
    await expect(page.locator('[data-testid="section-header-archived"]')).toBeVisible()

    // Archived section is collapsed by default — expand it to see the workspace
    await page.locator('[data-testid="section-header-archived"]').click()
    await expect(wsItem).toBeVisible()
  })

  test('should persist section collapse state across page reload', async ({ page, authenticatedWorkspace }) => {
    const wsItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)

    // Collapse the section
    await page.locator('[data-testid="section-header-in_progress"] > summary').click()
    await expect(wsItem).not.toBeVisible()

    // Reload the page
    await page.reload()
    await expect(page.locator('[data-testid="section-header-in_progress"]')).toBeVisible()
    // Wait for sidebar to fully render
    await page.waitForTimeout(500)

    // Section should still be collapsed after reload
    await expect(wsItem).not.toBeVisible()

    // Expand it again for cleanup
    await page.locator('[data-testid="section-header-in_progress"] > summary').click()
  })
})
