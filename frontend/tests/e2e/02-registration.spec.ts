import { expect, test } from './fixtures'
import { loginViaUI } from './helpers'

test.describe('Worker Registration', () => {
  // In standalone mode, the worker is auto-registered with name "Local".
  // These tests verify the worker appears online in the UI.

  test('should show worker online in new workspace dialog after approval', async ({ page }) => {
    // Verifies that the auto-registered worker appears online
    // in the new workspace dialog. The initial onMount fetch should find it.
    await loginViaUI(page)

    // Wait for the page to fully settle
    await page.waitForLoadState('networkidle')

    // Open the new workspace dialog
    await page.getByTitle(/New workspace/).first().click()
    await expect(page.getByRole('heading', { name: 'New Workspace' })).toBeVisible()

    // The initial fetch on mount should find the worker (already online).
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('button', { name: 'Create', exact: true })).toBeEnabled()

    // Verify the worker name appears in the dropdown (standalone uses "Local")
    await expect(page.locator('select').first()).toContainText('Local')
  })

  test('should refresh worker list when clicking refresh button', async ({ page }) => {
    await loginViaUI(page)

    // Open the new workspace dialog
    await page.getByTitle(/New workspace/).first().click()
    await expect(page.getByRole('heading', { name: 'New Workspace' })).toBeVisible()

    // Wait for initial load to find the worker
    await expect(page.locator('select').first()).toContainText('Local')

    // Click the refresh button
    await page.getByTitle('Refresh workers').click()

    // Worker should still be shown after refresh
    await expect(page.locator('select').first()).toContainText('Local')
  })

  test('should show updated worker list when dialog is re-opened', async ({ page }) => {
    await loginViaUI(page)

    // Open the new workspace dialog
    await page.getByTitle(/New workspace/).first().click()
    await expect(page.getByRole('heading', { name: 'New Workspace' })).toBeVisible()

    // Wait for initial load
    await expect(page.locator('select').first()).toContainText('Local')

    // Close the dialog
    await page.getByRole('button', { name: 'Cancel' }).click()
    await expect(page.getByRole('heading', { name: 'New Workspace' })).not.toBeVisible()

    // Re-open the dialog
    await page.getByTitle(/New workspace/).first().click()
    await expect(page.getByRole('heading', { name: 'New Workspace' })).toBeVisible()

    // The re-mount fetch should find the worker
    await expect(page.locator('select').first()).toContainText('Local')
  })
})
