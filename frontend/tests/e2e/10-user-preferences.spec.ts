import { expect, test } from './fixtures'
import { loginViaUI } from './helpers/ui'

test.describe('User Preferences', () => {
  test('should navigate to settings page', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/settings')
    await expect(page.getByText('Preferences')).toBeVisible()
  })

  test('should show browser tab by default with theme options', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/settings')
    await expect(page.getByRole('heading', { name: 'Theme', exact: true })).toBeVisible()
    // "This Browser" tab should show "Use account default" option
    await expect(page.getByRole('button', { name: 'Use account default' }).first()).toBeVisible()
  })

  test('should show profile fields in Account Defaults tab', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/settings')
    await expect(page.getByText('Preferences')).toBeVisible()

    // Switch to Account Defaults tab
    await page.getByRole('tab', { name: 'Account Defaults' }).click()

    // Should show profile and password sections
    await expect(page.getByRole('heading', { name: 'Profile' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Change Password' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Theme', exact: true })).toBeVisible()
  })

  test('should persist theme preference after page reload', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/settings')
    await expect(page.getByRole('heading', { name: 'Theme', exact: true })).toBeVisible()

    // Click "Dark" to set the browser-level theme to dark
    await page.getByRole('button', { name: 'Dark' }).first().click()

    // Verify localStorage was updated
    const themeValue = await page.evaluate(() => localStorage.getItem('leapmux-theme'))
    expect(themeValue).toBe('dark')

    // Reload the page
    await page.reload()
    await expect(page.getByText('Preferences')).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Theme', exact: true })).toBeVisible()

    // After reload, verify the theme persisted via localStorage
    const themeAfterReload = await page.evaluate(() => localStorage.getItem('leapmux-theme'))
    expect(themeAfterReload).toBe('dark')

    // Restore to account default to avoid affecting other tests
    await page.getByRole('button', { name: 'Use account default' }).first().click()
  })
})
