import { expect, test } from './fixtures'
import { loginViaUI, logoutViaUI } from './helpers/ui'

// Either landing spot for a successful login: `/`, or the `/workspace/{id}`
// AppShell's auto-activate effect replaces it with when the account owns one.
const APP_HOME_URL_RE = /\/(?:workspace\/[^/]+)?$/
const INVALID_CREDENTIALS_RE = /invalid|incorrect|wrong|failed/i
// A workspace id that does not exist. See the redirect test for why the
// round-trip can only be pinned by a path app home cannot resolve to on its own.
const MISSING_WORKSPACE_PATH = '/workspace/ws-nonexistent-redirect-target'

test.describe('Authentication', () => {
  test('should login with valid credentials', async ({ page }) => {
    await loginViaUI(page)
    // Verify URL redirected to app home
    await expect(page).toHaveURL(APP_HOME_URL_RE)
  })

  test('should show error with wrong password', async ({ page }) => {
    await page.goto('/login')
    await page.getByLabel('Username').fill('admin')
    await page.getByLabel('Password').fill('wrongpassword')
    await page.getByRole('button', { name: 'Sign in' }).click()

    // Should remain on the login page with an error. The error message is
    // asserted FIRST because it is the only assertion here that WAITS for the
    // login attempt to resolve -- a `not.toHaveURL` placed before it passes on
    // its first poll against the still-unchanged /login URL, whatever the hub
    // answered.
    await expect(page.getByText(INVALID_CREDENTIALS_RE)).toBeVisible()
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()
    // Still on /login. Asserted POSITIVELY rather than as
    // `not.toHaveURL(APP_HOME_URL_RE)`: that negative form was satisfied by
    // /workspace/{id} too, so once the fixture account owned a workspace it
    // would have passed whether the wrong password was rejected or silently
    // accepted -- the exact regression it exists to catch.
    await expect(page).toHaveURL(/\/login(?:\?.*)?$/)
  })

  test('should logout and return to login page', async ({ page }) => {
    await loginViaUI(page)

    // Use the user menu to logout
    await logoutViaUI(page)

    // Should return to login page
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()
    await expect(page.getByText('LeapMux')).toBeVisible()
  })

  test('should redirect to original page after login', async ({ page }) => {
    // Navigate to a protected page while unauthenticated.
    //
    // The target has to be a path app home CANNOT resolve to on its own, or
    // the test pins nothing: LoginPage falls back to `/` whenever it ignores
    // `?redirect=`, and AppShell's auto-activate effect then forwards `/` to
    // the user's first workspace. So `/` fails (it IS the fallback) and a real
    // `/workspace/{id}` fails too (both branches reach it). A workspace id
    // that does not exist is reachable only by honouring the parameter.
    await page.goto(MISSING_WORKSPACE_PATH)

    // Should redirect to login with redirect query param
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()
    expect(page.url()).toContain(`redirect=${encodeURIComponent(MISSING_WORKSPACE_PATH)}`)

    // Login
    await page.getByLabel('Username').fill('admin')
    await page.getByLabel('Password').fill('admin123')
    await page.getByRole('button', { name: 'Sign in' }).click()

    // Should redirect back to the original page, not to the `/` fallback.
    await expect(page).toHaveURL(new RegExp(`${MISSING_WORKSPACE_PATH}$`))
  })
})
