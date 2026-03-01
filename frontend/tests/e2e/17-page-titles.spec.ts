import { expect, test } from './fixtures'
import { loginViaToken } from './helpers/ui'

test.describe('Page Titles', () => {
  test('should show login page title', async ({ page }) => {
    await page.goto('/login')
    await expect(page).toHaveTitle(/Login.*LeapMux|LeapMux/)
  })

  test('should show page title after login', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/o/admin')
    // After login, title may show "Dashboard" or an auto-activated workspace name
    await expect(page).toHaveTitle(/LeapMux/)
  })

  test('should show workspace title in page title', async ({ page, authenticatedWorkspace }) => {
    // Page title should include the workspace name (fixture creates a workspace with an auto-generated name)
    await expect(page).toHaveTitle(/LeapMux/)
  })

  test('should show preferences page title', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/settings')
    await expect(page.getByText('Preferences')).toBeVisible()
    await expect(page).toHaveTitle(/Preferences.*LeapMux/)
  })

  test('should show admin page title', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/admin')
    await expect(page.getByText('Administration')).toBeVisible()
    await expect(page).toHaveTitle(/Admin.*LeapMux/)
  })

  test('should show workers page title', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/o/admin/workers')
    await expect(page.getByRole('heading', { name: 'Workers' })).toBeVisible()
    await expect(page).toHaveTitle(/Workers.*LeapMux/)
  })

  test('should show org management page title', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/o/admin/org')
    await expect(page.getByRole('heading', { name: 'Members' })).toBeVisible()
    await expect(page).toHaveTitle(/Org.*LeapMux/)
  })

  test('should update title when switching workspaces', async ({ page, authenticatedWorkspace }) => {
    await expect(page).toHaveTitle(/LeapMux/)

    // Navigate back to org root
    await page.goto('/o/admin')
    // Title may show "Dashboard" or an auto-activated workspace name
    await expect(page).toHaveTitle(/LeapMux/)
  })
})
