import type { UnseededDevServerHandle } from './helpers/devServer'
import { test as base, expect } from '@playwright/test'
import { getCurrentUser, listPasskeysViaAPI } from './helpers/api'
import { startUnseededDevServer, stopDevServer } from './helpers/devServer'
import { loginWithPasskeyViaUI, logoutViaUI, solveCaptchaViaUI } from './helpers/ui'
import { enableVirtualAuthenticator } from './helpers/webauthn'

/**
 * Uses a standalone unseeded dev server (no pre-registered admin) so we can
 * exercise the /setup flow. Scoped per test so each test sees a fresh
 * setup-mode instance; this file cannot use the shared fixtures from
 * fixtures.ts, because that fixture signs up `admin` automatically.
 */
// Playwright fixtures declare their dependencies by destructuring the first
// parameter; this fixture has no dependencies, hence the empty pattern.
// eslint-disable-next-line no-empty-pattern
async function setupServer({}: object, use: (server: UnseededDevServerHandle) => Promise<void>): Promise<void> {
  const server = await startUnseededDevServer({ dataDirPrefix: 'leapmux-e2e-setup' })
  try {
    await use(server)
  }
  finally {
    await stopDevServer(server)
  }
}

const test = base.extend<{ server: UnseededDevServerHandle }>({
  server: setupServer,
  baseURL: async ({ server }, use) => {
    await use(server.hubUrl)
  },
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

  // Every address, not only `/`. A hub with no account has nothing to sign in
  // to, nothing to sign up beside, no address to reset and no session to
  // verify or elevate, so each of these used to serve a form that cannot
  // succeed -- and four of them carried no check of any kind. SetupGate now
  // answers all of them from one place above the router outlet.
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
    await page.getByLabel('Username').fill('firstadmin')
    await page.getByLabel('Display Name').fill('First Admin')
    await page.getByLabel('New Password').fill('strongpass1')
    await page.getByLabel('Confirm Password').fill('strongpass1')
    await solveCaptchaViaUI(page)
    await page.getByRole('button', { name: 'Create account' }).click()
    await expect(page).toHaveURL(/\/$/)

    await logoutViaUI(page)
    await page.goto('/setup')
    await expect(page).toHaveURL(/\/login$/)
  })

  test('setup rejects reserved username "solo"', async ({ page }) => {
    await page.goto('/setup')
    await page.getByLabel('Username').fill('solo')
    await page.getByLabel('Display Name').fill('Solo')
    await page.getByLabel('New Password').fill('strongpass1')
    await page.getByLabel('Confirm Password').fill('strongpass1')
    await solveCaptchaViaUI(page)
    await page.getByRole('button', { name: 'Create account' }).click()
    await expect(page.getByText(/reserved username/i)).toBeVisible()
    await expect(page).toHaveURL(/\/setup$/)
  })

  test('setup accepts username "admin" and marks the user as admin', async ({ page, server, context }) => {
    await page.goto('/setup')
    await page.getByLabel('Username').fill('admin')
    await page.getByLabel('Display Name').fill('Admin')
    await page.getByLabel('New Password').fill('strongpass1')
    await page.getByLabel('Confirm Password').fill('strongpass1')
    await solveCaptchaViaUI(page)
    await page.getByRole('button', { name: 'Create account' }).click()
    // Flat home route: post-setup lands on `/` (the authenticated home),
    // not an org-scoped path. Matches APP_HOME_URL_RE in the auth specs.
    await expect(page).toHaveURL(/\/$/)

    // Verify the backend recorded this user as an admin.
    const cookies = await context.cookies()
    const session = cookies.find(c => c.name === 'leapmux-session')
    expect(session?.value).toBeTruthy()
    const user = await getCurrentUser(server.hubUrl, `leapmux-session=${session!.value}`)
    expect(user.isAdmin).toBe(true)
    expect(user.username).toBe('admin')
  })

  // The first administrator picks a credential the same way anybody else
  // does. The hub used to refuse BeginPasskeySignUp during initial setup and
  // the page hid the method pills to match; one change removed both. The
  // account is still an admin, and it still claims the reserved `admin` name.
  test('setup accepts a passkey for the first administrator', async ({ page, server, context }) => {
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

    const cookies = await context.cookies()
    const session = cookies.find(c => c.name === 'leapmux-session')
    expect(session?.value).toBeTruthy()
    const cookie = `leapmux-session=${session!.value}`
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
