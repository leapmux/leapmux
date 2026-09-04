import type { SoloServerHandle } from './helpers/devServer'
import { expect, test } from './fixtures'
import { startSoloServer, stopSoloServer } from './helpers/devServer'

/** A password the hub's own validator accepts. */
const SOLO_PASSWORD = 'correct-horse-battery-staple'
const SOLO_ENV = { LEAPMUX_HUB_DEV_FRONTEND: undefined }

/**
 * The password-setup screen, on a solo hub that answers beyond loopback.
 *
 * `-listen 0.0.0.0` is what exposes it; the spec still reaches it over
 * loopback, because a wildcard bind answers there too. That matters for CI: a
 * runner may hold no address of its own, and this needs none.
 */
test.describe('Password setup gate', () => {
  let solo: SoloServerHandle | undefined

  test.beforeEach(async () => {
    solo = await startSoloServer({ listenHost: '0.0.0.0', env: SOLO_ENV })
  })

  test.afterEach(async () => {
    await stopSoloServer(solo)
  })

  test('blocks the app until the account has a password', async ({ page }) => {
    await page.goto(`${solo!.hubUrl}/`)

    // The whole app, not a dismissible notice. This is the only protected
    // setup action that a passwordless TCP caller can use.
    const gate = page.getByTestId('password-setup-gate')
    await expect(gate).toBeVisible()
    await expect(gate).toContainText('TCP callers can only complete this setup')

    // Fixed to the single account: a free field could only be filled in with a
    // name that cannot sign in.
    const username = gate.getByLabel('Username')
    await expect(username).toHaveValue('solo')
    await expect(username).toHaveAttribute('readonly', '')

    const submit = gate.getByRole('button', { name: 'Set Password' })
    await expect(submit).toBeDisabled()

    await gate.getByLabel('New Password').fill(SOLO_PASSWORD)
    await gate.getByLabel('Confirm Password').fill(SOLO_PASSWORD)
    await expect(submit).toBeEnabled()
    await submit.click()

    // The app loads with NO further sign-in. Storing the password is what
    // started demanding one, so the reply hands this browser a session; without
    // it the page would be signed out of the form it just used.
    await expect(gate).toBeHidden()
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeHidden()

    // And the rule is armed for everybody ELSE. This browser keeps the session
    // it was handed, so the cookie has to go before the reload -- otherwise the
    // test would prove only that a signed-in browser stays signed in.
    await page.context().clearCookies()
    await page.reload()
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()
    await page.getByLabel('Password').fill(SOLO_PASSWORD)
    await page.getByRole('button', { name: 'Sign in' }).click()
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeHidden()
    await expect(page.getByTestId('password-setup-gate')).toBeHidden()
  })

  test('restricts loopback TCP to password setup too', async ({ page }) => {
    const loopbackOnly = await startSoloServer({ env: SOLO_ENV })
    try {
      await page.goto(`${loopbackOnly.hubUrl}/`)
      await expect(page.getByTestId('password-setup-gate')).toBeVisible()
      await expect(page.getByRole('button', { name: 'Sign in' })).toBeHidden()
    }
    finally {
      await stopSoloServer(loopbackOnly)
    }
  })
})
