import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { getBrowserPrefValue, loginViaToken, openPreferencesDialog, pickTheme } from './helpers/ui'

/**
 * Read a browser-prefs field as a JSON string, for the substring assertions
 * below. The read itself is `getBrowserPrefValue`, which every object-shaped
 * preference now shares -- this spec used to carry its own copy of it.
 */
async function getBrowserPrefJson(page: Page, field: string): Promise<string> {
  return JSON.stringify(await getBrowserPrefValue(page, field) ?? null)
}

/**
 * Read the resolved `--mono-font-family` AppShell applies. Custom properties
 * inherit, but the shell sets this one on an inner div, so scan for the
 * closest element that carries a value rather than guessing a selector.
 */
async function resolvedMonoFamily(page: import('@playwright/test').Page): Promise<string> {
  return page.evaluate(() => {
    for (const el of Array.from(document.querySelectorAll('*'))) {
      const v = getComputedStyle(el).getPropertyValue('--mono-font-family')
      if (v)
        return v
    }
    return ''
  })
}

test.describe('Preferences scope overrides', () => {
  test('overrides the theme on this device and clears back to the account default', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    await openPreferencesDialog(page, 'appearance')
    const dialog = page.getByRole('dialog', { name: 'Preferences' })

    const chip = dialog.getByTestId('scope-chip-appearance.theme')
    await expect(chip).toHaveText(/Account default/)

    await chip.click()
    await page.getByRole('menuitemradio', { name: 'Override on this device' }).click()
    await expect(chip).toHaveText(/This device/)

    const themeRow = dialog.locator('[data-setting-id="appearance.theme"]')
    await themeRow.getByRole('radiogroup', { name: 'Theme mode' }).getByRole('radio', { name: 'Dark' }).click()
    await expect.poll(() => getBrowserPrefValue(page, 'theme')).toEqual({ name: 'default', mode: 'dark' })

    // The palette is the other half of the same key, so it lands on the same
    // tier and in the same document.
    await pickTheme(themeRow, 'nord')
    await expect.poll(() => getBrowserPrefValue(page, 'theme')).toEqual({ name: 'nord', mode: 'dark' })

    await chip.click()
    await page.getByRole('menuitemradio', { name: 'Use account default' }).click()
    await expect(chip).toHaveText(/Account default/)
    await expect.poll(() => getBrowserPrefValue(page, 'theme')).toBeNull()
  })

  test('overrides the monospace font stack on this device and the UI follows', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    // The shell (which owns the font-family vars) mounts async; wait for
    // its carrier element before reading the resolved value.
    await page.waitForSelector('[style*="--mono-font-family"]')
    const before = await resolvedMonoFamily(page)
    await expect(before).toBeTruthy()

    await openPreferencesDialog(page, 'appearance')
    const dialog = page.getByRole('dialog', { name: 'Preferences' })

    // The whole {enabled, fonts} object is the override unit: switching the
    // toggle row onto the device tier overrides both halves at once.
    const chip = dialog.getByTestId('scope-chip-appearance.monoFonts')
    await chip.click()
    await page.getByRole('menuitemradio', { name: 'Override on this device' }).click()

    const toggleRow = dialog.locator('[data-setting-id="appearance.monoFonts"]')
    await toggleRow.locator('input[role="switch"]').check()

    const stackRow = dialog.locator('[data-setting-id="appearance.monoFontStack"]')
    const addFont = stackRow.getByRole('textbox', { name: 'Add monospace font' })
    await addFont.fill('E2EMonoFont')
    await addFont.press('Enter')
    await expect(stackRow.getByText('E2EMonoFont')).toBeVisible()

    // The stack is an ORDERED list -- the first name the browser has wins --
    // so the keyboard has to be able to reorder it. Reordering was bound to
    // `draggable` alone, which left the priority order to the mouse.
    const entries = stackRow.getByRole('button', { name: /^Rename / })
    await addFont.fill('E2ESecondFont')
    await addFont.press('Enter')
    await expect(entries).toHaveText(['E2EMonoFont', 'E2ESecondFont'])

    await stackRow.getByRole('button', { name: 'Rename E2ESecondFont' }).press('Alt+ArrowUp')
    await expect(entries).toHaveText(['E2ESecondFont', 'E2EMonoFont'])
    await stackRow.getByRole('button', { name: 'Rename E2ESecondFont' }).press('Alt+ArrowDown')
    await expect(entries).toHaveText(['E2EMonoFont', 'E2ESecondFont'])

    // The shell's resolved family now leads with the override (quoted), and
    // the override is persisted as a whole-object tier.
    await expect.poll(() => resolvedMonoFamily(page)).toContain('"E2EMonoFont"')
    await expect.poll(() => getBrowserPrefJson(page, 'monoFontOverride')).toContain('E2EMonoFont')

    // Clearing the override restores the previous family.
    await chip.click()
    await page.getByRole('menuitemradio', { name: 'Use account default' }).click()
    await expect.poll(() => resolvedMonoFamily(page)).toBe(before)
    await expect.poll(() => getBrowserPrefJson(page, 'monoFontOverride')).toBe('null')
  })
})
