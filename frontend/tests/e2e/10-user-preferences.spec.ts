import { expect, test } from './fixtures'
import { loginViaUI, openPreferencesDialog } from './helpers/ui'

test.describe('User Preferences', () => {
  test('should open preferences dialog', async ({ page }) => {
    await loginViaUI(page)
    await openPreferencesDialog(page)
    await expect(page.getByRole('dialog', { name: 'Preferences' })).toBeVisible()
  })

  test('should show browser tab by default with theme options', async ({ page }) => {
    await loginViaUI(page)
    await openPreferencesDialog(page)
    await expect(page.getByRole('heading', { name: 'Theme', exact: true })).toBeVisible()
    // "This Browser" tab should show "Use account default" option
    await expect(page.getByRole('button', { name: 'Use account default' }).first()).toBeVisible()
  })

  test('should show appearance settings in Account Defaults tab', async ({ page }) => {
    await loginViaUI(page)
    await openPreferencesDialog(page)

    // Switch to Account Defaults tab
    await page.getByRole('tab', { name: 'Account Defaults' }).click()

    // Should show appearance and font settings
    await expect(page.getByRole('heading', { name: 'Theme', exact: true })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Fonts' })).toBeVisible()
  })

  test('should persist theme preference after page reload', async ({ page }) => {
    await loginViaUI(page)
    await openPreferencesDialog(page)
    await expect(page.getByRole('heading', { name: 'Theme', exact: true })).toBeVisible()

    // Click "Dark" to set the browser-level theme to dark
    await page.getByRole('button', { name: 'Dark' }).first().click()

    // Verify localStorage was updated
    const themeValue = await page.evaluate(() => localStorage.getItem('leapmux:theme'))
    expect(themeValue).toBe('dark')

    // Reload the page and re-open dialog
    await page.reload()
    await openPreferencesDialog(page)
    await expect(page.getByRole('heading', { name: 'Theme', exact: true })).toBeVisible()

    // After reload, verify the theme persisted via localStorage
    const themeAfterReload = await page.evaluate(() => localStorage.getItem('leapmux:theme'))
    expect(themeAfterReload).toBe('dark')

    // Restore to account default to avoid affecting other tests
    await page.getByRole('button', { name: 'Use account default' }).first().click()
  })
})
