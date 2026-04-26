import { expect, test } from './fixtures'
import { loginViaUI, logoutViaUI } from './helpers/ui'

const ORG_ADMIN_URL_RE = /\/o\/admin/
const LOGIN_URL_RE = /\/login/

/**
 * The "Sign in disabled while fields are empty" UI state and the absence of a
 * `leapmux_token` localStorage entry are unit-tested in
 * `src/components/common/LoginPage.test.tsx`. The redirect-when-unauth and
 * AuthGuard behavior are unit-tested in `tests/unit/components/AuthGuard.test.tsx`.
 *
 * What only a real browser session can verify is that the auth cookie is
 * HttpOnly and survives `localStorage.clear()` + reload — proving the session
 * is genuinely cookie-backed, not localStorage-backed. That smoke is below.
 */
test.describe('Auth Edge Cases', () => {
  test('cookie-based session survives localStorage.clear() and reload', async ({ page }) => {
    await loginViaUI(page)
    await expect(page).toHaveURL(ORG_ADMIN_URL_RE)

    // Clear localStorage entirely. If the session were localStorage-backed,
    // the next reload would bounce to /login.
    await page.evaluate(() => localStorage.clear())

    await page.reload()
    await expect(page).toHaveURL(ORG_ADMIN_URL_RE)
    await expect(page).not.toHaveURL(LOGIN_URL_RE)

    // Logout still works after the localStorage clear (cookie is the source of truth).
    await logoutViaUI(page)
    await page.goto('/o/admin')
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()
  })
})
