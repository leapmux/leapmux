import { expect, test } from './fixtures'
import { loginViaUI, logoutViaUI, solveCaptchaViaUI } from './helpers/ui'

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

  // The credential pages are for a visitor who is NOT signed in, and until
  // SignedOutOnly they had no gate at all: a signed-in user got the whole
  // form on every one of them. /signup was the worst of the four -- it gates
  // only on the hub's signup setting, so the user could create a SECOND
  // account and the page then swapped their session to it without a word.
  //
  // In the real router, not only in the unit test: the gate depends on route
  // wrappers, and a page that lost its wrapper would still pass every unit
  // test of the component.
  test('sends a signed-in user away from every credential page', async ({ page }) => {
    await loginViaUI(page)
    await expect(page).toHaveURL(APP_HOME_URL_RE)

    for (const path of ['/login', '/signup', '/forgot-password', '/setup']) {
      await page.goto(path)
      await expect(page).toHaveURL(APP_HOME_URL_RE)
      await expect(page.getByRole('button', { name: 'Sign in' })).toBeHidden()
    }
  })

  // /reset-password is the one page that EXPLAINS instead of redirecting, and
  // the reason is in its address: it carries a single-use token and no
  // ?redirect=, so a silent bounce spends nothing, says nothing, and the
  // `replace` takes the tokened address out of that tab's history as well.
  test('explains rather than redirects on the reset-password page', async ({ page }) => {
    await loginViaUI(page)
    await expect(page).toHaveURL(APP_HOME_URL_RE)

    await page.goto('/reset-password?token=not-a-real-token')
    await expect(page.getByTestId('signed-out-only-explain')).toBeVisible()
    await expect(page.getByTestId('signed-out-only-sign-out')).toBeVisible()

    // Signing out re-renders the form at the SAME address, token intact.
    await page.getByTestId('signed-out-only-sign-out').click()
    await expect(page.getByTestId('signed-out-only-explain')).toBeHidden()
    await expect(page).toHaveURL(/\/reset-password\?token=not-a-real-token$/)
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
