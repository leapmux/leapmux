import { expect, test } from './fixtures'
import { loginViaUI } from './helpers'

test.describe('Diff View Preferences', () => {
  test('should show Diff View section in This Browser tab with correct options', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/settings')
    await expect(page.getByRole('heading', { name: 'Diff View' }).first()).toBeVisible()
    await expect(page.getByRole('button', { name: 'Unified' }).first()).toBeVisible()
    await expect(page.getByRole('button', { name: 'Side-by-Side' }).first()).toBeVisible()
  })

  test('should persist browser-level diff view in localStorage', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/settings')
    await expect(page.getByRole('heading', { name: 'Diff View' }).first()).toBeVisible()

    // Click "Unified" in browser tab (first occurrence)
    await page.getByRole('button', { name: 'Unified' }).first().click()
    let value = await page.evaluate(() => localStorage.getItem('leapmux-diff-view'))
    expect(value).toBe('unified')

    // Click "Side-by-Side" in browser tab (first occurrence)
    await page.getByRole('button', { name: 'Side-by-Side' }).first().click()
    value = await page.evaluate(() => localStorage.getItem('leapmux-diff-view'))
    expect(value).toBe('split')

    // Click "Use account default" within the Diff View section.
    // In the browser tab, "Use account default" buttons appear for: Theme, Terminal Theme,
    // Diff View, Turn End Sound. The Diff View one is the 3rd (0-indexed: 2).
    await page.getByRole('button', { name: 'Use account default' }).nth(2).click()
    value = await page.evaluate(() => localStorage.getItem('leapmux-diff-view'))
    expect(value).toBe('account-default')

    // Reload and verify persistence
    await page.reload()
    await expect(page.getByText('Preferences')).toBeVisible()
    const valueAfterReload = await page.evaluate(() => localStorage.getItem('leapmux-diff-view'))
    expect(valueAfterReload).toBe('account-default')
  })

  test('should show Diff View section in Account Defaults tab', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/settings')
    await page.getByRole('tab', { name: 'Account Defaults' }).click()
    await expect(page.getByRole('heading', { name: 'Diff View' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Unified' }).first()).toBeVisible()
    await expect(page.getByRole('button', { name: 'Side-by-Side' }).first()).toBeVisible()
  })

  test('should persist account-level diff view via API', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/settings')
    await page.getByRole('tab', { name: 'Account Defaults' }).click()
    await expect(page.getByRole('heading', { name: 'Diff View' })).toBeVisible()

    // Click "Side-by-Side"
    await page.getByRole('button', { name: 'Side-by-Side' }).first().click()

    // Wait for API call to complete
    await page.waitForTimeout(500)

    // Reload and verify persistence
    await page.reload()
    await expect(page.getByText('Preferences')).toBeVisible()
    await page.getByRole('tab', { name: 'Account Defaults' }).click()
    await expect(page.getByRole('heading', { name: 'Diff View' })).toBeVisible()

    // Wait for preferences to load from API
    await page.waitForTimeout(500)

    // Restore to "Unified" to clean up
    await page.getByRole('button', { name: 'Unified' }).first().click()
    await page.waitForTimeout(500)
  })
})
