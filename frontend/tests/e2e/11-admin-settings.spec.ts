import { expect, test } from './fixtures'
import { loginViaUI } from './helpers'

test.describe('Admin Settings', () => {
  test('should access admin page as admin user', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/admin')
    // Should see admin settings
    await expect(page.getByText('System Settings')).toBeVisible()
  })

  test('should show user management section', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/admin')
    await expect(page.getByText('User Management')).toBeVisible()
  })

  test('should toggle signup enabled setting and persist', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/admin')
    await expect(page.getByText('System Settings')).toBeVisible()

    // Locate the Sign-up enabled switch label (the label is the clickable element,
    // since Kobalte's <label> intercepts pointer events on the underlying <input>)
    const signupSwitch = page.getByRole('switch', { name: 'Sign-up enabled' })
    await expect(signupSwitch).toBeVisible()
    // Wait for settings to be loaded from API (signup is enabled by global setup,
    // but the signal initializes to false until the API response arrives)
    await expect(signupSwitch).toBeChecked()
    const initialChecked = await signupSwitch.isChecked()

    // Toggle the switch by clicking its label
    await page.getByText('Sign-up enabled').click()

    // Wait for the toggle to take effect
    if (initialChecked) {
      await expect(signupSwitch).not.toBeChecked()
    }
    else {
      await expect(signupSwitch).toBeChecked()
    }
    const afterToggle = await signupSwitch.isChecked()

    // Save settings
    await page.getByRole('button', { name: 'Save Settings' }).click()
    await expect(page.getByText('Settings saved.')).toBeVisible()

    // Reload and verify the setting persisted
    await page.reload()
    await expect(page.getByText('System Settings')).toBeVisible()
    const afterReload = await page.getByRole('switch', { name: 'Sign-up enabled' }).isChecked()
    expect(afterReload).toBe(afterToggle)

    // Restore original state: toggle back and save
    await page.getByText('Sign-up enabled').click()
    await page.getByRole('button', { name: 'Save Settings' }).click()
    await expect(page.getByText('Settings saved.')).toBeVisible()
  })

  test('should show user list in admin panel', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/admin')
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
    const adminRow = page.getByRole('row', { name: /^admin\s+Admin/ })
    await expect(adminRow).toBeVisible()

    // The admin user's role column should show the Admin badge
    await expect(adminRow.getByText('Admin').first()).toBeVisible()
  })
})
