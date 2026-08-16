import { expect, test } from './fixtures'
import { loginViaUI, logoutViaUI, solveCaptchaViaUI } from './helpers/ui'

// Where a successful login lands, and stays: `/` is the whole app, and
// activating a workspace no longer changes the URL.
const APP_HOME_URL_RE = /\/$/
const LOGIN_URL_RE = /\/login/

/**
 * The "Sign in disabled while fields are empty" UI state and the absence of a
 * `leapmux_token` localStorage entry are unit-tested in
 * `src/components/common/LoginPage.test.tsx`. The redirect-when-unauth and
 * AuthGuard behavior are unit-tested in `src/components/common/AuthGuard.test.tsx`.
 *
 * What only a real browser session can verify is that the auth cookie is
 * HttpOnly and survives `localStorage.clear()` + reload — proving the session
 * is genuinely cookie-backed, not localStorage-backed. That smoke is below.
 */
test.describe('Auth Edge Cases', () => {
  test('cookie-based session survives localStorage.clear() and reload', async ({ page }) => {
    await loginViaUI(page)
    await expect(page).toHaveURL(APP_HOME_URL_RE)

    // Clear localStorage entirely. If the session were localStorage-backed,
    // the next reload would bounce to /login.
    await page.evaluate(() => localStorage.clear())

    await page.reload()
    await expect(page).toHaveURL(APP_HOME_URL_RE)
    await expect(page).not.toHaveURL(LOGIN_URL_RE)

    // Logout still works after the localStorage clear (cookie is the source of truth).
    await logoutViaUI(page)
    await page.goto('/')
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()
  })

  // solveCaptchaViaUI is a no-op when the hub reports captcha disabled;
  // that return lives INSIDE its poll loop (the submit button enabling).
  // When neither the widget nor an enabled submit appears — here a
  // GetSystemInfo that never answers keeps the submit gate closed — the
  // helper must throw at the wait instead of silently proceeding, so a
  // widget-mount regression fails at its cause rather than as a starved
  // downstream assertion.
  test('solveCaptchaViaUI fails loudly when neither widget nor enabled submit appears', async ({ page }) => {
    await page.route('**/leapmux.v1.AuthService/GetSystemInfo*', () => new Promise(() => {}))
    await page.goto('/login')

    await expect(solveCaptchaViaUI(page)).rejects.toThrow(/solveCaptchaViaUI/)
  })
})
