import { expect, test } from './fixtures'
import { loginViaUI, logoutViaUI } from './helpers'

test.describe('Auth Edge Cases', () => {
  test('should disable sign in button with empty username', async ({ page }) => {
    await page.goto('/login')
    // Only fill in the password field, leave username empty
    await page.getByLabel('Password').fill('admin')

    // The Sign in button should be disabled when username is empty
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeDisabled()
    await expect(page).not.toHaveURL(/\/o\//)
  })

  test('should disable sign in button with empty password', async ({ page }) => {
    await page.goto('/login')
    // Only fill in the username field, leave password empty
    await page.getByLabel('Username').fill('admin')

    // The Sign in button should be disabled when password is empty
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeDisabled()
    await expect(page).not.toHaveURL(/\/o\//)
  })

  test('should persist session across page refresh', async ({ page }) => {
    await loginViaUI(page)
    await expect(page).toHaveURL(/\/o\/admin/)

    // Reload the page and verify we are still authenticated
    await page.reload()
    await expect(page).toHaveURL(/\/o\/admin/)
    await expect(page).not.toHaveURL(/\/login/)
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
