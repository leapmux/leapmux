import { expect, test } from './fixtures'
import { signUpViaUI } from './helpers'

test.describe('Sign Up', () => {
  test('should sign up with valid credentials and redirect', async ({ page }) => {
    // Use a unique username so this test doesn't conflict with "newuser"
    // created in global setup or other test runs
    const username = `signup-${Date.now()}`
    await signUpViaUI(page, username, 'password123', 'Signup Test User', 'signup@test.com')
    // Should redirect to personal org after signup
    await expect(page).toHaveURL(new RegExp(`/o/${username}`))
  })

  test('should show error for duplicate username', async ({ page }) => {
    await signUpViaUI(page, 'admin', 'password123')
    // Should show an error (username taken)
    await expect(page.getByText('username already taken')).toBeVisible()
  })

  test('should link back to login page', async ({ page }) => {
    await page.goto('/signup')
    await page.getByText('Sign in').click()
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()
  })
})
