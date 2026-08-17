import { KEY_BROWSER_PREFS } from '../../src/lib/browserStorage'
import { expect, test } from './fixtures'
import { getBrowserPref, loginViaToken, openPreferencesDialog } from './helpers/ui'

/**
 * Read a browser-prefs field as JSON. `getBrowserPref` stringifies scalars;
 * the font override is an object, so read the raw wrapped blob and serialize
 * the field for substring assertions.
 */
async function getBrowserPrefJson(page: import('@playwright/test').Page, field: string): Promise<string> {
  return page.evaluate(([key, f]) => {
    const raw = localStorage.getItem(key)
    if (!raw)
      return 'null'
    const wrapper = JSON.parse(raw)
    const prefs = wrapper?.v
    if (prefs == null || typeof prefs !== 'object' || prefs[f] === undefined)
      return 'null'
    return JSON.stringify(prefs[f])
  }, [KEY_BROWSER_PREFS, field] as const)
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

    await dialog.getByRole('radiogroup', { name: 'Theme', exact: true }).getByRole('radio', { name: 'Dark' }).click()
    await expect.poll(() => getBrowserPref(page, 'theme')).toBe('dark')

    await chip.click()
    await page.getByRole('menuitemradio', { name: 'Use account default' }).click()
    await expect(chip).toHaveText(/Account default/)
    await expect.poll(() => getBrowserPref(page, 'theme')).toBeNull()
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
