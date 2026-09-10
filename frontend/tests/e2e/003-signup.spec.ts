import { expect, test } from './fixtures'
import { signUpViaUI } from './helpers/ui'

// SignupForm unit tests cover reserved names. SignupPage unit tests cover the login link.
test.describe('signup', () => {
  test('creates an account and opens the app', async ({ page }) => {
    const username = `signup-${Date.now()}`
    await signUpViaUI(page, username, 'password123', 'Signup Test User', 'signup@test.com')
    await expect(page).toHaveURL('/')
  })

  test('reports the username when that username already exists', async ({ page }) => {
    await signUpViaUI(page, 'newuser', 'password123')
    await expect(page.getByText(/username "newuser" is already taken/)).toBeVisible()
  })
})
