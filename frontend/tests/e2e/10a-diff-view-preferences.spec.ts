import { expect, test } from './fixtures'
import { getBrowserPref, loginViaUI, openPreferencesDialog } from './helpers/ui'

test.describe('Diff View Preferences', () => {
  test('should show Diff View section in This Browser tab with correct options', async ({ page }) => {
    await loginViaUI(page)
    await openPreferencesDialog(page)
    await expect(page.getByRole('heading', { name: 'Diff View' }).first()).toBeVisible()
    await expect(page.getByRole('button', { name: 'Unified' }).first()).toBeVisible()
    await expect(page.getByRole('button', { name: 'Side-by-Side' }).first()).toBeVisible()
  })

  test('should persist browser-level diff view in localStorage', async ({ page }) => {
    await loginViaUI(page)
    await openPreferencesDialog(page)
    await expect(page.getByRole('heading', { name: 'Diff View' }).first()).toBeVisible()

    // Click "Unified" in browser tab (first occurrence)
    await page.getByRole('button', { name: 'Unified' }).first().click()
    let value = await getBrowserPref(page, 'diffView')
    expect(value).toBe('unified')

    // Click "Side-by-Side" in browser tab (first occurrence)
    await page.getByRole('button', { name: 'Side-by-Side' }).first().click()
    value = await getBrowserPref(page, 'diffView')
    expect(value).toBe('split')

    // Click "Use account default" within the Diff View section.
    // In the browser tab, "Use account default" buttons appear for: Theme, Terminal Theme,
    // Diff View, Turn End Sound. The Diff View one is the 3rd (0-indexed: 2).
    await page.getByRole('button', { name: 'Use account default' }).nth(2).click()
    value = await getBrowserPref(page, 'diffView')
    expect(value).toBeNull()

    // Reload and verify persistence
    await page.reload()
    await openPreferencesDialog(page)
    const valueAfterReload = await getBrowserPref(page, 'diffView')
    expect(valueAfterReload).toBeNull()
  })

  test('should show Diff View section in Account Defaults tab', async ({ page }) => {
    await loginViaUI(page)
    await openPreferencesDialog(page)
    await page.getByRole('tab', { name: 'Account Defaults' }).click()
    await expect(page.getByRole('heading', { name: 'Diff View' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Unified' }).first()).toBeVisible()
    await expect(page.getByRole('button', { name: 'Side-by-Side' }).first()).toBeVisible()
  })
})
