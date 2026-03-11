import { expect, test } from './fixtures'
import { loginViaUI, logoutViaUI } from './helpers/ui'

const ORG_URL_RE = /\/o\//
const ORG_ADMIN_URL_RE = /\/o\/admin/
const LOGIN_URL_RE = /\/login/

test.describe('Auth Edge Cases', () => {
  test('should disable sign in button with empty username', async ({ page }) => {
    await page.goto('/login')
    // Only fill in the password field, leave username empty
    await page.getByLabel('Password').fill('admin')

    // The Sign in button should be disabled when username is empty
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeDisabled()
    await expect(page).not.toHaveURL(ORG_URL_RE)
  })

  test('should disable sign in button with empty password', async ({ page }) => {
    await page.goto('/login')
    // Only fill in the username field, leave password empty
    await page.getByLabel('Username').fill('admin')

    // The Sign in button should be disabled when password is empty
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeDisabled()
    await expect(page).not.toHaveURL(ORG_URL_RE)
  })

  test('should persist session across page refresh', async ({ page }) => {
    await loginViaUI(page)
    await expect(page).toHaveURL(ORG_ADMIN_URL_RE)

    // Reload the page and verify we are still authenticated
    await page.reload()
    await expect(page).toHaveURL(ORG_ADMIN_URL_RE)
    await expect(page).not.toHaveURL(LOGIN_URL_RE)
  })

  test('should redirect unauthenticated user to login', async ({ page }) => {
    // Navigate to a protected route without logging in
    await page.goto('/o/admin')

    // Should redirect to the login page
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()
  })

  test('should clear session on logout', async ({ page }) => {
    await loginViaUI(page)
    await logoutViaUI(page)

    // Try navigating to a protected route
    await page.goto('/o/admin')

    // Should stay on the login page
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()
  })
})
