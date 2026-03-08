import { expect, test } from './fixtures'
import { loginViaUI } from './helpers/ui'

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

    // Should contain the worker card with name fetched via E2EE
    // (standalone sets LEAPMUX_WORKER_NAME=Local in E2E fixtures)
    await expect(ownedSection.getByTestId('worker-name').filter({ hasText: 'Local' })).toBeVisible()
  })

  test('should show worker status badge', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/o/admin/workers')
    await expect(page.getByRole('heading', { name: 'Workers' })).toBeVisible()

    const card = page.getByTestId('worker-card').first()
    // Status badge should show Online or Offline
    await expect(card.getByTestId('worker-status')).toBeVisible()
  })
})
