import { expect, test } from './fixtures'
import { loginViaUI } from './helpers/ui'
import { openNewWorkspaceDialog } from './helpers/worktree'

test.describe('Worker Registration', () => {
  // In dev mode, the worker is auto-registered with name "Local".
  // These tests verify the worker appears online in the UI.

  test('should show worker online in new workspace dialog after approval', async ({ page }) => {
    // Verifies that the auto-registered worker appears online
    // in the new workspace dialog. The initial onMount fetch should find it.
    await loginViaUI(page)

    // Through the shared helper: the section header's `+` is a MENU now, so
    // "New workspace..." is an item inside it and the accessible name this
    // used to click no longer exists on any button.
    await openNewWorkspaceDialog(page)

    // The initial fetch on mount should find the worker (already online).
    // Verify the worker name appears in the dropdown (dev mode uses "Local")
    await expect(page.getByTestId('worker-select-menu-trigger')).toContainText('Local')
  })

  test('should refresh worker list when clicking refresh button', async ({ page }) => {
    await loginViaUI(page)

    // Through the shared helper: the section header's `+` is a MENU now, so
    // "New workspace..." is an item inside it and the accessible name this
    // used to click no longer exists on any button.
    await openNewWorkspaceDialog(page)

    // Wait for initial load to find the worker
    await expect(page.getByTestId('worker-select-menu-trigger')).toContainText('Local')

    // Click the refresh button
    await page.getByLabel('Refresh workers').click()

    // Worker should still be shown after refresh
    await expect(page.getByTestId('worker-select-menu-trigger')).toContainText('Local')
  })

  test('should not spin refresh button when selecting a directory', async ({ page }) => {
    await loginViaUI(page)

    // Through the shared helper: the section header's `+` is a MENU now, so
    // "New workspace..." is an item inside it and the accessible name this
    // used to click no longer exists on any button.
    await openNewWorkspaceDialog(page)

    // Wait for initial load to find the worker and directory tree
    await expect(page.getByTestId('worker-select-menu-trigger')).toContainText('Local')
    await expect(page.getByTestId('tree-root-node')).toBeVisible()

    // Locate the "Refresh directory tree" button's icon
    const refreshBtn = page.getByLabel('Refresh directory tree')
    const refreshIcon = refreshBtn.locator('svg')

    // Click a directory node in the tree (the root node)
    await page.getByTestId('tree-root-node').click()

    // The refresh button's icon should NOT have a spinning animation
    const animation = await refreshIcon.evaluate(
      el => window.getComputedStyle(el).animationName,
    )
    expect(animation).toBe('none')
  })

  test('should show updated worker list when dialog is re-opened', async ({ page }) => {
    await loginViaUI(page)

    // Through the shared helper: the section header's `+` is a MENU now, so
    // "New workspace..." is an item inside it and the accessible name this
    // used to click no longer exists on any button.
    await openNewWorkspaceDialog(page)

    // Wait for initial load
    await expect(page.getByTestId('worker-select-menu-trigger')).toContainText('Local')

    // Close the dialog
    await page.getByRole('button', { name: 'Cancel' }).click()
    await expect(page.getByRole('heading', { name: 'New Workspace' })).not.toBeVisible()

    // Re-open the dialog
    await openNewWorkspaceDialog(page)

    // The re-mount fetch should find the worker
    await expect(page.getByTestId('worker-select-menu-trigger')).toContainText('Local')
  })
})
