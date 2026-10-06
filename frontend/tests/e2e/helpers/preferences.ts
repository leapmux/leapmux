import type { Locator, Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { openAppAs, openSettingsAt, pickTheme } from './ui'

/**
 * The Preferences dialog as the E2E specs drive it: the opening of the dialog, the scope chip of a dual row, and the
 * theme controls of the Appearance section.
 * `./ui.ts` holds the opening of the dialog itself (`openSettingsAt`) and the palette menu (`pickTheme`).
 */

/** Sign in with `token`, load the app, and open Preferences at `category`. Return the dialog. */
export async function openPreferencesAs(page: Page, token: string, category?: string): Promise<Locator> {
  await openAppAs(page, token)
  return openSettingsAt(page, category)
}

/** The tier that a dual preference row edits: an override on this device, or the default of the account. */
export type PreferenceScope = 'device' | 'account'

/** The menu item that selects each tier, and the text that the chip shows for it. */
const SCOPES: Record<PreferenceScope, { item: string, chip: RegExp }> = {
  device: { item: 'Override on this device', chip: /This device/ },
  account: { item: 'Use account default', chip: /Account default/ },
}

/** The scope chip of the dual preference row `settingId`, such as `appearance.theme`. */
export function preferenceScopeChip(page: Page, settingId: string): Locator {
  return page.getByTestId(`scope-chip-${settingId}`)
}

/**
 * Switch the dual preference row `settingId` to `scope` through its chip, and require the chip to show that tier.
 * The account tier deletes the override of this device: it writes no copy of the account value.
 */
export async function setPreferenceScope(page: Page, settingId: string, scope: PreferenceScope): Promise<void> {
  const chip = preferenceScopeChip(page, settingId)
  await chip.click()
  await page.getByRole('menuitemradio', { name: SCOPES[scope].item }).click()
  await expect(chip, `the ${settingId} row edits the ${scope} tier`).toHaveText(SCOPES[scope].chip)
}

/**
 * The radio of `mode` in the mode group `groupName` of a theme row, such as `Theme mode` and `Dark`.
 * The three theme rows each carry one group: `Theme mode`, `Terminal theme mode`, and `Syntax theme mode`.
 */
export function themeModeRadio(row: Locator, groupName: string, mode: string): Locator {
  return row.getByRole('radiogroup', { name: groupName }).getByRole('radio', { name: mode })
}

/** Pick `mode` in the mode group `groupName` of a theme row. The caller asserts the effect. */
export async function pickThemeMode(row: Locator, groupName: string, mode: string): Promise<void> {
  await themeModeRadio(row, groupName, mode).click()
}

/**
 * Pin the theme of the app to an override on this device with the palette `palette`, through the Preferences dialog,
 * require the page to paint it, and close the dialog.
 */
export async function overrideThemeOnThisDevice(page: Page, palette: string): Promise<void> {
  const dialog = await openSettingsAt(page, 'appearance')
  await setPreferenceScope(page, 'appearance.theme', 'device')
  await pickTheme(dialog.locator('[data-setting-id="appearance.theme"]'), palette)
  await expect(page.locator('html'), 'the page paints the palette of the override').toHaveAttribute('data-ui-theme', palette)
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
}
