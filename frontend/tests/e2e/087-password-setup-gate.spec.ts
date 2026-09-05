import type { SoloServerHandle } from './helpers/devServer'
import { expect, test } from './fixtures'
import { startSoloServer, stopSoloServer } from './helpers/devServer'

/** A password the hub's own validator accepts. */
const SOLO_PASSWORD = 'correct-horse-battery-staple'

/**
 * The password-setup screen, on a solo hub reached over TCP.
 *
 * The LISTENER no longer decides it: every TCP address restricts a passwordless
 * caller to the setup procedure, and the last test below pins that for
 * loopback. `-listen 0.0.0.0` stays here because the exposed case is the one an
 * operator meets first; the spec still reaches it over loopback, because a
 * wildcard bind answers there too. That matters for CI: a runner may hold no
 * address of its own, and this needs none.
 */
test.describe('Password setup gate', () => {
  let solo: SoloServerHandle | undefined

  test.beforeEach(async () => {
    solo = await startSoloServer({ listenHost: '0.0.0.0' })
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

    // The app loads with NO further sign-in. This browser held no session at
    // all -- the setup procedure is the one thing a passwordless TCP caller
    // may call -- so the reply's cookie is what carries it into the app.
    // Without it the operator would set a password and then sign in with it.
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
    const loopbackOnly = await startSoloServer()
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
