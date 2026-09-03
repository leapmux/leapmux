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

    const dialog = await openSettingsAt(page, 'account')
    const row = dialog.locator('[data-setting-id="account.password"]')
    await expect(row).toBeVisible()
    await expect(dialog.locator('[data-setting-id="account.profile"]')).toHaveCount(0)
    await expect(dialog.locator('[data-setting-id="account.passkeys"]')).toHaveCount(0)
    await expect(dialog.locator('[data-setting-id="account.linkedProviders"]')).toHaveCount(0)

    // "Set", not "Change": this account holds no password, whatever its
    // users.password_set column claims. The hub reports the stored hash.
    const submit = row.getByRole('button', { name: 'Set Password' })
    await expect(submit).toBeDisabled()

    // And the row states what this first password arms, because it reaches
    // every address rather than this account alone.
    await expect(row.getByText(/asks every network address for a sign-in as “solo”/)).toBeVisible()

    await row.getByLabel('New Password').fill(SOLO_PASSWORD)
    await row.getByLabel('Confirm Password').fill(SOLO_PASSWORD)
    await expect(submit).toBeEnabled()
    await submit.click()

    await expect(row.getByText('Password set.')).toBeVisible()
    // The account is re-read, so the row now offers the other operation, and
    // the warning goes: replacing a password arms nothing further.
    await expect(row.getByRole('button', { name: 'Change Password' })).toBeVisible()
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
