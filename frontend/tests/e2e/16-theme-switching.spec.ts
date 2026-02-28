import { expect, test } from './fixtures'
import { loginViaUI, setInitialTheme } from './helpers/ui'

test.describe('Theme Switching', () => {
  test('should show theme options on preferences page', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/settings')

    // Theme section should be visible (exact: true to avoid matching "Terminal Theme")
    await expect(page.getByRole('heading', { name: 'Theme', exact: true })).toBeVisible()

    // Should show theme options (use .first() since Terminal Theme also has Dark/Light buttons)
    await expect(page.getByRole('button', { name: 'Light' }).first()).toBeVisible()
    await expect(page.getByRole('button', { name: 'Dark' }).first()).toBeVisible()
    await expect(page.getByRole('button', { name: 'System' })).toBeVisible()
  })

  test('should apply dark theme', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/settings')
    await expect(page.getByRole('heading', { name: 'Theme', exact: true })).toBeVisible()

    // Click "Dark" theme button (first one is in Theme section, not Terminal Theme)
    await page.getByRole('button', { name: 'Dark' }).first().click()

    // Theme should be stored in localStorage
    const theme = await page.evaluate(() => localStorage.getItem('leapmux-theme'))
    expect(theme).toBe('dark')
  })

  test('should apply light theme', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/settings')
    await expect(page.getByRole('heading', { name: 'Theme', exact: true })).toBeVisible()

    // Click "Light" theme button (first one is in Theme section, not Terminal Theme)
    await page.getByRole('button', { name: 'Light' }).first().click()

    // Theme should be stored in localStorage
    const theme = await page.evaluate(() => localStorage.getItem('leapmux-theme'))
    expect(theme).toBe('light')
  })

  test('should persist theme across page reload', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/settings')
    await expect(page.getByRole('heading', { name: 'Theme', exact: true })).toBeVisible()

    // Switch to dark theme (first Dark button is in Theme section)
    await page.getByRole('button', { name: 'Dark' }).first().click()
    await page.waitForTimeout(500)

    // Reload the page
    await page.reload()
    await expect(page.getByRole('heading', { name: 'Theme', exact: true })).toBeVisible()

    // Theme should still be dark (check localStorage)
    const theme = await page.evaluate(() => localStorage.getItem('leapmux-theme'))
    expect(theme).toBe('dark')
  })

  test('should set initial theme via helper', async ({ page }) => {
    // Set dark theme before navigation
    await setInitialTheme(page, 'dark')

    await loginViaUI(page)

    // Check that the theme was applied
    const theme = await page.evaluate(() => localStorage.getItem('leapmux-theme'))
    expect(theme).toBe('dark')
  })
})
