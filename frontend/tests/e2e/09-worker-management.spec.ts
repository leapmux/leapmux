import { expect, test } from './fixtures'
import { loginViaUI } from './helpers'

test.describe('Worker Management', () => {
  test('should show Workers link in user menu and navigate to page', async ({ page }) => {
    await loginViaUI(page)
    // Click the user menu trigger
    await page.getByTestId('user-menu-trigger').first().click()
    // Workers menu item should be visible
    const workersItem = page.getByRole('menuitem', { name: 'Workers' })
    await expect(workersItem).toBeVisible()
    await workersItem.click()
    // Should navigate to workers page
    await expect(page).toHaveURL(/\/o\/admin\/workers/)
    await expect(page.getByRole('heading', { name: 'Workers' })).toBeVisible()
  })

  test('should show My Workers section with registered worker', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/o/admin/workers')
    await expect(page.getByRole('heading', { name: 'Workers' })).toBeVisible()

    // My Workers section should exist
    const ownedSection = page.getByTestId('worker-section-owned')
    await expect(ownedSection).toBeVisible()
    await expect(ownedSection.getByText('My Workers')).toBeVisible()

    // Should contain the Local worker card (standalone auto-registers with name "Local")
    await expect(ownedSection.getByTestId('worker-name').filter({ hasText: 'Local' })).toBeVisible()
  })

  test('should show worker properties (hostname, status)', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/o/admin/workers')
    await expect(page.getByRole('heading', { name: 'Workers' })).toBeVisible()

    const card = page.getByTestId('worker-card').first()
    // Hostname should be visible
    await expect(card.getByTestId('worker-hostname')).toBeVisible()
    // Status badge should show Online or Offline
    await expect(card.getByTestId('worker-status')).toBeVisible()
  })

  test('should rename a worker', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/o/admin/workers')
    await expect(page.getByRole('heading', { name: 'Workers' })).toBeVisible()

    // Open context menu
    await page.getByTestId('worker-menu-trigger').first().click()
    await page.getByRole('menuitem', { name: 'Rename' }).click()

    // Rename dialog should appear
    await expect(page.getByRole('heading', { name: 'Worker Settings' })).toBeVisible()
    const input = page.getByTestId('rename-input')
    await expect(input).toBeVisible()

    // Clear and type new name
    await input.fill('renamed-worker')
    await page.getByRole('button', { name: 'Save' }).click()

    // Dialog should close and new name should appear
    await expect(page.getByRole('heading', { name: 'Worker Settings' })).not.toBeVisible()
    await expect(page.getByTestId('worker-name').filter({ hasText: 'renamed-worker' })).toBeVisible()

    // Rename back to original name
    await page.getByTestId('worker-menu-trigger').first().click()
    await page.getByRole('menuitem', { name: 'Rename' }).click()
    await expect(page.getByTestId('rename-input')).toBeVisible()
    await page.getByTestId('rename-input').fill('Local')
    await page.getByRole('button', { name: 'Save' }).click()
    await expect(page.getByRole('heading', { name: 'Worker Settings' })).not.toBeVisible()
    await expect(page.getByTestId('worker-name').filter({ hasText: 'Local' })).toBeVisible()
  })
})
