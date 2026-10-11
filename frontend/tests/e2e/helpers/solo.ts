import type { Fixtures, Locator, Page } from '@playwright/test'
import type { SoloServerHandle } from './devServer'
import { expect } from '@playwright/test'
import { withCleanup } from './cleanup'
import { startSoloServer, stopSoloServer } from './devServer'

/**
 * A private `leapmux solo` hub for a spec, and the steps that every solo spec takes through its password gate.
 * A solo hub holds one account, `solo`. A TCP caller meets the password-setup gate until that account has a password,
 * and the sign-in form after that.
 */

/** A password that the validator of the hub accepts. */
export const SOLO_PASSWORD = 'correct-horse-battery-staple'

/** The fixtures of a spec that runs against a private solo hub. */
export interface SoloServerFixtures {
  /**
   * The host for `-listen`. The default 127.0.0.1 accepts only local connections. A spec of an exposed hub sets
   * `0.0.0.0` with `test.use`, and reaches it over loopback, which the wildcard listener also serves.
   */
  soloListenHost: string
  /** A solo hub for this test alone. The fixture stops it and removes its data after the test. */
  soloServer: SoloServerHandle
}

/** Spread these into `test.extend` to give a spec a private solo hub for each test that asks for one. */
export const soloServerFixtures: Fixtures<SoloServerFixtures> = {
  soloListenHost: ['127.0.0.1', { option: true }],
  soloServer: async ({ soloListenHost }, use) => {
    const solo = await startSoloServer({ listenHost: soloListenHost })
    await withCleanup(() => use(solo), () => stopSoloServer(solo))
  },
}

/** Fill both password fields of the setup form under `scope` with {@link SOLO_PASSWORD}. */
export async function fillSoloPasswordSetup(scope: Page | Locator): Promise<void> {
  await scope.getByLabel('New Password').fill(SOLO_PASSWORD)
  await scope.getByLabel('Confirm Password').fill(SOLO_PASSWORD)
}

/**
 * Set the first password of the solo account through the password-setup gate, and require the gate to go away.
 * The reply of the setup carries a session cookie, so the app loads with no further sign-in.
 */
export async function completeSoloPasswordSetup(page: Page): Promise<void> {
  const gate = page.getByTestId('password-setup-gate')
  await expect(gate, 'a TCP caller meets the password-setup gate').toBeVisible()
  await fillSoloPasswordSetup(gate)
  await gate.getByRole('button', { name: 'Set Password' }).click()
  await expect(gate, 'the setup stores the password and lets the app load').toBeHidden()
}

/**
 * Prove that the hub now asks every browser with no session for the password, and sign in with it.
 *
 * The cookies go first. The setup handed this browser a session, so a bare reload would prove only that a signed-in
 * browser stays signed in. The form fixes the username to `solo`, because a solo hub has exactly one account.
 */
export async function signInToSoloViaUI(page: Page): Promise<void> {
  await page.context().clearCookies()
  await page.reload()
  const signIn = page.getByRole('button', { name: 'Sign in' })
  await expect(signIn, 'a browser with no session meets the sign-in form').toBeVisible()
  const username = page.getByLabel('Username')
  await expect(username).toHaveValue('solo')
  await expect(username).toHaveAttribute('readonly', '')
  await page.getByLabel('Password').fill(SOLO_PASSWORD)
  await signIn.click()
  await expect(signIn, 'the password signs in').toBeHidden()
}
