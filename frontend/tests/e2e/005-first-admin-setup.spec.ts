import type { Page } from '@playwright/test'
import type { UnseededDevServerHandle } from './helpers/devServer'
import { expect } from '@playwright/test'
import { devServerTest } from './dev-server-fixtures'
import { getCurrentUser, listPasskeysViaAPI } from './helpers/api'
import { solveCaptchaViaUI } from './helpers/captcha'
import { startUnseededDevServer, stopDevServer } from './helpers/devServer'
import { loginWithPasskeyViaUI, logoutViaUI, readSessionCookie } from './helpers/ui'
import { enableVirtualAuthenticator } from './helpers/webauthn'

/**
 * Fill the password form of /setup for a new account, solve its captcha, and submit it.
 * The caller asserts the outcome: the app, or the refusal of the name.
 */
async function submitSetupForm(page: Page, username: string, displayName: string): Promise<void> {
  await page.getByLabel('Username').fill(username)
  await page.getByLabel('Display Name').fill(displayName)
  await page.getByLabel('New Password').fill('strongpass1')
  await page.getByLabel('Confirm Password').fill('strongpass1')
  await solveCaptchaViaUI(page)
  await page.getByRole('button', { name: 'Create account' }).click()
}

/**
 * Each test starts a new unseeded dev server, which has no administrator, so the
 * test sees a fresh instance in setup mode. This file cannot use the shared
 * fixtures of fixtures.ts, because the suite hub signs up `admin` at its start.
 */
const test = devServerTest<UnseededDevServerHandle>(async (use) => {
  const server = await startUnseededDevServer({ dataDirPrefix: 'leapmux-e2e-setup' })
  try {
    await use(server)
  }
  finally {
    await stopDevServer(server)
  }
})

test.describe('First-admin setup', () => {
  test('root path redirects to /setup on a fresh instance', async ({ page }) => {
    await page.goto('/')
    await expect(page).toHaveURL(/\/setup$/)
    await expect(page.getByRole('heading', { name: /Welcome to LeapMux/i })).toBeVisible()

    // No theme control before an account exists: the app stores a theme per
    // account, and this page is what runs when there is not one yet. The page
    // still paints -- the default palette, at whatever the OS asks for.
    //
    // This check lives in this test rather than in one of its own: the fixture
    // in this file boots a dedicated unseeded dev instance per `test()`.
    await expect(page.getByTestId('theme-chooser')).toHaveCount(0)
    await expect(page.locator('html')).toHaveAttribute('data-ui-theme', 'default')
  })

  // SetupGate must protect every credential route while no account exists.
  // Login, signup, and recovery forms cannot succeed in that state. Neither session verification nor elevation can succeed.
  // The shared check above the router outlet sends every route to setup.
  const DEAD_END_PATHS = [
    '/',
    '/login',
    '/login?redirect=%2F',
    '/signup',
    '/recover-account',
    '/recover-account/complete?token=whatever',
    '/verify-email',
    '/elevate',
    '/auth/idp/complete-signup?token=whatever',
    '/no-such-page',
  ]

  // One test rather than one per path: the fixture boots a dedicated unseeded
  // hub for every `test()`, and the redirect is a client-side decision that a
  // single session can exercise for every address in turn.
  test('every other path redirects to /setup on a fresh instance', async ({ page }) => {
    for (const path of DEAD_END_PATHS) {
      await page.goto(path)
      await expect(page, `${path} must lead to /setup`).toHaveURL(/\/setup$/)
      await expect(page.getByRole('heading', { name: /Welcome to LeapMux/i })).toBeVisible()
    }
  })

  // The mirror rule, and the reason it moved out of SetupPage: the page read
  // the setup state from onMount, before the system info arrived, so a
  // cold load answered the fabricated "setup required" and bounced /setup to
  // /login and straight back.
  test('/setup gives way to /login once an administrator exists', async ({ page }) => {
    await page.goto('/setup')
    await submitSetupForm(page, 'firstadmin', 'First Admin')
    await expect(page).toHaveURL(/\/$/)

    await logoutViaUI(page)
    await page.goto('/setup')
    await expect(page).toHaveURL(/\/login$/)
  })

  test('setup rejects reserved username "solo"', async ({ page }) => {
    await page.goto('/setup')
    await submitSetupForm(page, 'solo', 'Solo')
    await expect(page.getByText(/reserved username/i)).toBeVisible()
    await expect(page).toHaveURL(/\/setup$/)
  })

  test('setup accepts username "admin" and marks the user as admin', async ({ page, server }) => {
    await page.goto('/setup')
    await submitSetupForm(page, 'admin', 'Admin')
    // Flat home route: post-setup lands on `/` (the authenticated home),
    // not an org-scoped path. Matches APP_HOME_URL_RE in the auth specs.
    await expect(page).toHaveURL(/\/$/)

    // Verify the backend recorded this user as an admin.
    const user = await getCurrentUser(server.hubUrl, await readSessionCookie(page, 'the setup'))
    expect(user.isAdmin).toBe(true)
    expect(user.username).toBe('admin')
  })

  // The first administrator picks a credential the same way anybody else
  // does. The hub used to refuse BeginPasskeySignUp during initial setup and
  // the page hid the method pills to match; one change removed both. The
  // account is still an admin, and it still claims the reserved `admin` name.
  test('setup accepts a passkey for the first administrator', async ({ page, server }) => {
    await enableVirtualAuthenticator(page)

    await page.goto('/setup')
    await page.getByLabel('Username').fill('admin')
    await page.getByLabel('Display Name').fill('Admin')
    await page.getByLabel('Email').fill('admin@test.local')
    await page.getByRole('radio', { name: 'Passkey' }).click()
    // The password fields belong to the other method and must be gone, or the
    // form asks the first administrator for a credential it will not use.
    await expect(page.getByLabel('New Password')).toHaveCount(0)
    await solveCaptchaViaUI(page)
    await page.getByRole('button', { name: 'Sign up with passkey' }).click()
    await expect(page).toHaveURL(/\/$/)

    const cookie = await readSessionCookie(page, 'the passkey setup')
    const user = await getCurrentUser(server.hubUrl, cookie)
    expect(user.isAdmin).toBe(true)
    expect(user.username).toBe('admin')
    // The ceremony stored a real credential, not just a session.
    expect(await listPasskeysViaAPI(server.hubUrl, cookie)).toHaveLength(1)

    // And it is the way back in. This account has NO password -- nothing on
    // this page asked for one -- so a passkey that signs up but cannot sign in
    // would lock the hub's only administrator out of it.
    await logoutViaUI(page)
    await loginWithPasskeyViaUI(page, 'admin')
  })
})
