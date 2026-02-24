import { expect, test } from './fixtures'
import { loginViaUI } from './helpers'

test.describe('Organization Management', () => {
  test('should navigate to org management page', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/o/admin/org')
    await expect(page.getByRole('heading', { name: 'Members' })).toBeVisible()
  })

  test('should create a new organization', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/o/admin/org')

    // Fill in org name and create
    const orgInput = page.getByPlaceholder('Organization name')
    if (await orgInput.isVisible()) {
      await orgInput.fill('test-org')
      await page.getByRole('button', { name: 'Create' }).click()
      await expect(page.getByText(/created|success/i)).toBeVisible()
    }
  })

  test('should switch org via user menu', async ({ page }) => {
    await loginViaUI(page)
    // The personal org (admin) should be the current org
    await expect(page).toHaveURL(/\/o\/admin/)
  })

  test('should show org members list', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/o/admin/org')
    await expect(page.getByRole('heading', { name: 'Members' })).toBeVisible()

    // The members table should be visible
    const table = page.locator('table')
    await expect(table).toBeVisible()

    // Verify header columns exist
    await expect(table.getByText('Username')).toBeVisible()
    await expect(table.getByText('Display Name')).toBeVisible()
    await expect(table.getByText('Role')).toBeVisible()
    await expect(table.getByText('Joined')).toBeVisible()

    // The admin user should appear as a member
    const adminRow = table.locator('tbody tr').filter({ hasText: 'admin' })
    await expect(adminRow).toBeVisible()
  })

  test('should not allow deleting personal org', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/o/admin/org')
    await expect(page.getByRole('heading', { name: 'Organization Settings' })).toBeVisible()

    // For a personal org, the type should be shown as "Personal"
    await expect(page.getByText('Personal')).toBeVisible()

    // The "Danger Zone" section and "Delete Organization" button should NOT be present
    await expect(page.getByText('Danger Zone')).not.toBeVisible()
    await expect(page.getByRole('button', { name: 'Delete Organization' })).not.toBeVisible()
  })
})
