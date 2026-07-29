import { expect, test } from './fixtures'
import { loginViaUI, logoutViaUI } from './helpers/ui'

// Where a successful login lands, and stays: `/` is the whole app, and
// activating a workspace no longer changes the URL.
const APP_HOME_URL_RE = /\/$/
const INVALID_CREDENTIALS_RE = /invalid|incorrect|wrong|failed/i
// App home carrying a query the app ignores. See the redirect test for why the
// round-trip can only be pinned by something `/` cannot produce on its own.
const REDIRECT_PROBE_PATH = '/?redirectProbe=1'

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
    // `not.toHaveURL(APP_HOME_URL_RE)`: `/login` does not end in `/`, so the
    // negative form passes on its first poll no matter what the hub answered,
    // whether the wrong password was rejected or silently accepted -- the exact
    // regression it exists to catch.
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
    // The target has to be something app home CANNOT produce on its own, or the
    // test pins nothing: LoginPage falls back to `/` whenever it ignores
    // `?redirect=`, so a bare `/` would pass either way. `/` is now the only
    // guarded path, which leaves the QUERY as the distinguishing part — and it
    // is not a contrivance: `?newWorkspace=true&workerId=…` is a real deep link
    // AppShell acts on, so losing the query here loses that.
    await page.goto(REDIRECT_PROBE_PATH)

    // Should redirect to login with redirect query param
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()
    expect(page.url()).toContain(`redirect=${encodeURIComponent(REDIRECT_PROBE_PATH)}`)

    // Login
    await page.getByLabel('Username').fill('admin')
    await page.getByLabel('Password').fill('admin123')
    await page.getByRole('button', { name: 'Sign in' }).click()

    // Should redirect back to the original page, query intact, not to the bare
    // `/` fallback.
    await expect(page).toHaveURL(/\/\?redirectProbe=1$/)
  })
})
