import { expect, test } from './fixtures'

/**
 * The context menu's item visibility (Rename / Share / Archive vs. Unarchive /
 * Delete / Move-to) and the owner-only filter are unit-tested in
 * `tests/unit/components/WorkspaceContextMenu.test.tsx`. This e2e exercises
 * the parts that need a real session + backend round-trip: inline rename
 * persisting via UpdateWorkspace, and the two-step Delete dialog actually
 * removing the workspace from the sidebar after the server processes it.
 */
test.describe('Workspace Context Menu', () => {
  test('rename via context menu and delete via two-step confirm round-trip the backend', async ({ page, authenticatedWorkspace }) => {
    const workspaceItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
    await expect(workspaceItem).toBeVisible()

    // ── Rename ──────────────────────────────────────────────────────────────
    await workspaceItem.hover()
    await workspaceItem.locator('button').first().click()
    await page.getByRole('menuitem', { name: 'Rename' }).click()

    const renameInput = workspaceItem.locator('input')
    await expect(renameInput).toBeVisible()
    await expect(renameInput).toBeFocused()
    await renameInput.fill('Renamed Workspace')
    await renameInput.press('Enter')

    await expect(renameInput).not.toBeVisible()
    await expect(page.getByText('Renamed Workspace')).toBeVisible()

    // ── Delete (two-step) ───────────────────────────────────────────────────
    // Navigate to the dashboard so the workspace can be safely deleted.
    await page.goto('/o/admin')
    await expect(workspaceItem).toBeVisible()

    await workspaceItem.hover()
    await workspaceItem.locator('button').first().click()
    await page.getByRole('menuitem', { name: 'Delete' }).click()

    const dialog = page.locator('dialog')
    await expect(dialog).toBeVisible()
    await dialog.getByRole('button', { name: 'Delete' }).click()
    await dialog.getByRole('button', { name: 'Confirm?' }).click()

    await expect(workspaceItem).not.toBeVisible()
  })
})
