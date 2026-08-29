import type { Page } from '@playwright/test'
import { accountStorageKeyPrefix, KEY_CHANNEL_RELAY_SEQ, KEY_USER_EVENTS_RELAY_SEQ } from '../../src/lib/browserStorage'
import { expect, test } from './fixtures'
import { loginViaToken, openSettingsAt, pickTheme } from './helpers/ui'

/**
 * Two accounts sharing one browser must not share stored state.
 *
 * Every `leapmux:` key is scoped to the signed-in account
 * (`leapmux:u:<userId>:<name>`). Before that, one flat
 * `leapmux:browser-prefs` document held the whole device tier for the whole
 * browser profile and nothing cleared it on sign-out, so the second account to
 * use a browser inherited the first account's overrides -- and the Preferences
 * dialog reported them, correctly, as "This device" over that user's own
 * account values.
 *
 * This is the end-to-end statement of the fix, and it has no equivalent
 * anywhere else in the suite: the unit tests drive `setStorageAccount`
 * directly, while this drives a real sign-out and sign-in through the app.
 */

/** Every `leapmux:` key in the page, so the assertions can talk about namespaces. */
async function storageKeys(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const keys: string[] = []
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i)
      if (key?.startsWith('leapmux:'))
        keys.push(key)
    }
    return keys.sort()
  })
}

/** Pin the theme to a device override and set the palette, through the dialog. */
async function overrideThemeOnThisDevice(page: Page, palette: string) {
  const dialog = await openSettingsAt(page, 'appearance')

  const chip = dialog.getByTestId('scope-chip-appearance.theme')
  await chip.click()
  await page.getByRole('menuitemradio', { name: 'Override on this device' }).click()
  await expect(chip).toHaveText(/This device/)

  const themeRow = dialog.locator('[data-setting-id="appearance.theme"]')
  await pickTheme(themeRow, palette)
  await expect(page.locator('html')).toHaveAttribute('data-ui-theme', palette)

  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
}

test.describe('account storage isolation', () => {
  test('a second account does not inherit the first account device overrides', async ({ page, leapmuxServer }) => {
    // The admin picks a device-tier palette.
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    await overrideThemeOnThisDevice(page, 'nord')

    const adminPrefix = accountStorageKeyPrefix(leapmuxServer.adminUserId)
    const adminKeys = await storageKeys(page)
    expect(adminKeys.some(k => k.startsWith(adminPrefix))).toBe(true)
    // Nothing of the app's own lives outside a namespace except the two
    // device-wide relay marks, which fence a process-wide sidecar and cannot be
    // partitioned. Both names come from the registry, so a rename cannot leave
    // this allowlist quietly accepting a key that is no longer either of them.
    const deviceScoped = [KEY_CHANNEL_RELAY_SEQ, KEY_USER_EVENTS_RELAY_SEQ].map(n => `leapmux:${n}`)
    for (const key of adminKeys)
      expect(key.startsWith('leapmux:u:') || deviceScoped.includes(key), key).toBe(true)

    // The second account signs in to the SAME browser, with no reload between
    // the two sessions beyond the navigation the sign-in performs.
    await loginViaToken(page, leapmuxServer.newuserToken)
    await page.goto('/')

    // They get the shipped default, not the admin's palette.
    //
    // The DIALOG assertion goes first, and it is the load-bearing one: it
    // cannot pass until the identity has resolved and the preferences have
    // seeded, because the scope chip is rendered from that state. `themeStore`
    // paints `data-ui-theme="default"` at import time, so asserting the
    // attribute alone would match the pre-auth boot frame and hold even if this
    // account HAD inherited the admin's override.
    const dialog = await openSettingsAt(page, 'appearance')
    await expect(dialog.getByTestId('scope-chip-appearance.theme')).toHaveText(/Account default/)
    await expect(page.locator('html')).toHaveAttribute('data-ui-theme', 'default')
    await page.keyboard.press('Escape')

    // The admin's keys are still there, byte for byte. The second account
    // neither read them nor cleared them, and the page-load sweep kept them:
    // another account's fresh key is registered and unexpired, so it survives.
    //
    // Note what is NOT asserted: that the second account wrote a namespace of
    // its own. It changed nothing, and the device tier stores only what
    // DIFFERS from the default, so a user who accepts the defaults writes no
    // key at all -- the isolation is in the keys that stayed put.
    const afterSecondSignIn = await storageKeys(page)
    const adminOwned = adminKeys.filter(k => k.startsWith(adminPrefix))
    expect(adminOwned.length).toBeGreaterThan(0)
    expect(afterSecondSignIn).toEqual(expect.arrayContaining(adminOwned))

    // And the admin comes back to exactly what they left.
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    await expect(page.locator('html')).toHaveAttribute('data-ui-theme', 'nord')
  })
})
