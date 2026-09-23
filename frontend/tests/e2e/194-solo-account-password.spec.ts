import type { SoloServerHandle } from './helpers/devServer'
import { expect, test } from './fixtures'
import { startSoloServer, stopSoloServer } from './helpers/devServer'
import { openSettingsAt } from './helpers/ui'

/** A password the hub's own validator accepts. */
const SOLO_PASSWORD = 'correct-horse-battery-staple'

/**
 * Preferences → Account → Password, on a real `leapmux solo` hub.
 *
 * A solo instance of its own, not the shared `leapmux dev` fixture: this is
 * the account state only solo produces -- one account, reachable with no
 * credentials, holding no password at all. Dev mode has a real password from
 * the first request.
 *
 * The row is the ONE Account row solo keeps, because ChangePassword is the one
 * account verb solo does not refuse. Its four neighbours stay hidden: solo
 * offers no sign-up, no passkey, no recovery and no provider link.
 */
test.describe('Account password in solo mode', () => {
  let solo: SoloServerHandle | undefined

  test.beforeEach(async () => {
    solo = await startSoloServer()
  })

  test.afterEach(async () => {
    await stopSoloServer(solo)
  })

  test('sets the first password, then signs in with it', async ({ page }) => {
    await page.goto(`${solo!.hubUrl}/`)

    // The GATE, not Preferences. A solo hub reached over TCP holds the whole
    // app behind this setup page until the account has a password, so there is
    // no app menu to open yet -- the test used to look for one and time out.
    // This page IS the first-password flow for a TCP caller.
    await expect(page.getByRole('heading', { name: 'Set a password to continue' })).toBeVisible()
    // It states what the first password arms, because it reaches every address
    // rather than this account alone.
    await expect(page.getByText(/Every TCP address asks for the password after setup/)).toBeVisible()

    const gateSubmit = page.getByRole('button', { name: 'Set Password' })
    await expect(gateSubmit).toBeDisabled()
    await page.getByLabel('New Password').fill(SOLO_PASSWORD)
    await page.getByLabel('Confirm Password').fill(SOLO_PASSWORD)
    await expect(gateSubmit).toBeEnabled()
    await gateSubmit.click()

    // The app loads once the password exists, and Account then offers the OTHER
    // operation. Its four neighbours stay hidden: solo offers no sign-up, no
    // passkey, no recovery and no provider link.
    const dialog = await openSettingsAt(page, 'account')
    const row = dialog.locator('[data-setting-id="account.password"]')
    await expect(row).toBeVisible()
    await expect(dialog.locator('[data-setting-id="account.profile"]')).toHaveCount(0)
    await expect(dialog.locator('[data-setting-id="account.passkeys"]')).toHaveCount(0)
    await expect(dialog.locator('[data-setting-id="account.linkedProviders"]')).toHaveCount(0)

    await expect(row.getByRole('button', { name: 'Change Password' })).toBeVisible()
    // The warning goes with the gate: replacing a password arms nothing further.
    await expect(row.getByText(/asks every network address for a sign-in as “solo”/)).toBeHidden()

    // The snapshot is re-read too: Network access asks for a first password
    // beside the addresses it guards, and it must stop asking now. Applying an
    // address there would otherwise replace the password just stored.
    const network = await openSettingsAt(page, 'admin-network')
    const addresses = network.locator('[data-setting-id="extra_listen_addresses"]')
    await expect(addresses.getByText(/Change it in Account → Password/)).toBeVisible()
    await expect(addresses.getByLabel('New Password')).toHaveCount(0)

    // And the password SIGNS IN. Storing it is what started demanding one, so
    // this browser keeps the session the reply handed it -- the cookie has to
    // go first, or the reload would prove only that a signed-in browser stays
    // signed in.
    await page.context().clearCookies()
    await page.reload()
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()
    await page.getByLabel('Password').fill(SOLO_PASSWORD)
    await page.getByRole('button', { name: 'Sign in' }).click()
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeHidden()
  })
})
