import { expect, test } from './fixtures'
import { solveCaptchaViaUI } from './helpers/captcha'
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
    await solveCaptchaViaUI(page)
    await page.getByRole('button', { name: 'Sign in' }).click()

    // Should remain on the login page with an error. The test asserts the
    // error message FIRST because it is the only assertion here that WAITS for
    // the login attempt to resolve -- a `not.toHaveURL` placed before it passes
    // on its first poll against the still-unchanged /login URL, whatever the
    // hub answered.
    await expect(page.getByText(INVALID_CREDENTIALS_RE)).toBeVisible()
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()
    // Verify the login URL after the error appears.
    // A negative home-URL check could pass before the hub answers, even if it accepts the wrong password.
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

  // SignedOutOnly restricts credential pages to visitors without a session.
  // Without that wrapper, a signed-in visitor could create another account and replace their active session without an explanation.
  // Check the real router because component tests alone cannot detect a missing route wrapper.
  test('sends a signed-in user away from every credential page', async ({ page }) => {
    await loginViaUI(page)
    await expect(page).toHaveURL(APP_HOME_URL_RE)

    for (const path of ['/login', '/signup', '/recover-account', '/setup']) {
      await page.goto(path)
      await expect(page).toHaveURL(APP_HOME_URL_RE)
      await expect(page.getByRole('button', { name: 'Sign in' })).toBeHidden()
    }
  })

  // Recovery completion explains the signed-in state instead of redirecting.
  // The URL holds a single-use token and no redirect parameter. A replacement navigation would remove that token URL from the tab history.
  test('explains rather than redirects on the recover-account completion page', async ({ page }) => {
    await loginViaUI(page)
    await expect(page).toHaveURL(APP_HOME_URL_RE)

    await page.goto('/recover-account/complete?token=not-a-real-token')
    await expect(page.getByTestId('signed-out-only-explain')).toBeVisible()
    await expect(page.getByTestId('signed-out-only-sign-out')).toBeVisible()

    // Signing out re-renders the form at the SAME address, token intact.
    await page.getByTestId('signed-out-only-sign-out').click()
    await expect(page.getByTestId('signed-out-only-explain')).toBeHidden()
    await expect(page).toHaveURL(/\/recover-account\/complete\?token=not-a-real-token$/)
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
    await solveCaptchaViaUI(page)
    await page.getByRole('button', { name: 'Sign in' }).click()

    // Should redirect back to the original page, query intact, not to the bare
    // `/` fallback.
    await expect(page).toHaveURL(/\/\?redirectProbe=1$/)
  })
})
