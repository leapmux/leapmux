import type { SoloServerFixtures } from './helpers/solo'
import { test as base, expect } from './fixtures'
import { withCleanup } from './helpers/cleanup'
import { startSoloServer, stopSoloServer } from './helpers/devServer'
import { fillSoloPasswordSetup, signInToSoloViaUI, soloServerFixtures } from './helpers/solo'

const test = base.extend<SoloServerFixtures>(soloServerFixtures)

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
  test.use({ soloListenHost: '0.0.0.0' })

  test('blocks the app until the account has a password', async ({ page, soloServer: solo }) => {
    await page.goto(`${solo.hubUrl}/`)

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

    await fillSoloPasswordSetup(gate)
    await expect(submit).toBeEnabled()
    await submit.click()

    // The app loads with NO further sign-in. This browser held no session at
    // all -- the setup procedure is the one thing a passwordless TCP caller
    // may call -- so the reply's cookie is what carries it into the app.
    // Without it the operator would set a password and then sign in with it.
    await expect(gate).toBeHidden()
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeHidden()

    // And the rule is armed for everybody ELSE. The helper drops the session
    // that this browser was handed before it reloads.
    await signInToSoloViaUI(page)
    await expect(page.getByTestId('password-setup-gate')).toBeHidden()
  })

  // A hub of its own on loopback alone, which the `0.0.0.0` hub of this file
  // cannot show.
  test('restricts loopback TCP to password setup too', async ({ page }) => {
    const loopbackOnly = await startSoloServer()
    await withCleanup(async () => {
      await page.goto(`${loopbackOnly.hubUrl}/`)
      await expect(page.getByTestId('password-setup-gate')).toBeVisible()
      await expect(page.getByRole('button', { name: 'Sign in' })).toBeHidden()
    }, () => stopSoloServer(loopbackOnly))
  })
})
