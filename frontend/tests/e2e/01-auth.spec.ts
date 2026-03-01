import { expect, test } from './fixtures'
import { loginViaUI, logoutViaUI } from './helpers/ui'

test.describe('Authentication', () => {
  test('should login with valid credentials', async ({ page }) => {
    await loginViaUI(page)
    // Verify URL redirected to personal org
    await expect(page).toHaveURL(/\/o\/admin/)
  })

  test('should show error with wrong password', async ({ page }) => {
    await page.goto('/login')
    await page.getByLabel('Username').fill('admin')
    await page.getByLabel('Password').fill('wrongpassword')
    await page.getByRole('button', { name: 'Sign in' }).click()

    // Should remain on the login page with an error
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()
    // Should NOT redirect to a workspace page (login failed)
    await expect(page).not.toHaveURL(/\/o\//)
    // Verify an error message is displayed (not just URL check)
    await expect(page.getByText(/invalid|incorrect|wrong|failed/i)).toBeVisible()
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
    // Navigate to a protected page while unauthenticated
    const targetPath = '/settings'
    await page.goto(targetPath)

    // Should redirect to login with redirect query param
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()
    expect(page.url()).toContain('redirect=')

    // Login
    await page.getByLabel('Username').fill('admin')
    await page.getByLabel('Password').fill('admin')
    await page.getByRole('button', { name: 'Sign in' }).click()

    // Should redirect back to the original page (settings/preferences)
    await expect(page).toHaveURL(/\/settings/)
    await expect(page.getByText('Preferences')).toBeVisible()
  })
})
