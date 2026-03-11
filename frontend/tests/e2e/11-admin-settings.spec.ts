import { expect, test } from './fixtures'
import { loginViaUI, openAdminDialog } from './helpers/ui'

const ADMIN_ROW_RE = /^admin\s+Admin/

test.describe('Admin Settings', () => {
  test('should access admin dialog as admin user', async ({ page }) => {
    await loginViaUI(page)
    await openAdminDialog(page)
    // Should see user management (system settings UI has been removed)
    await expect(page.getByText('User Management')).toBeVisible()
  })

  test('should show user list in admin panel', async ({ page }) => {
    await loginViaUI(page)
    await openAdminDialog(page)
    await expect(page.getByText('User Management')).toBeVisible()

    // The user table should be visible with at least one row
    const table = page.locator('table')
    await expect(table).toBeVisible()

    // Verify header columns exist
    await expect(table.getByText('Username')).toBeVisible()
    await expect(table.getByText('Display Name')).toBeVisible()
    await expect(table.getByText('Role')).toBeVisible()
    await expect(table.getByText('Actions')).toBeVisible()

    // The admin user should appear in the table.
    // Use getByRole('row') with a name pattern matching the admin row's accessible name
    // to avoid matching rows that contain "admin" in button text like "Make Admin".
    const adminRow = page.getByRole('row', { name: ADMIN_ROW_RE })
    await expect(adminRow).toBeVisible()

    // The admin user's role column should show the Admin badge
    await expect(adminRow.getByText('Admin').first()).toBeVisible()
  })
})
