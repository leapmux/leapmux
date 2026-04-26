import { expect, test } from './fixtures'
import { loginViaUI, logoutViaUI } from './helpers/ui'

const ORG_ADMIN_URL_RE = /\/o\/admin/
const ORG_URL_RE = /\/o\//
const INVALID_CREDENTIALS_RE = /invalid|incorrect|wrong|failed/i

test.describe('Authentication', () => {
  test('should login with valid credentials', async ({ page }) => {
    await loginViaUI(page)
    // Verify URL redirected to personal org
    await expect(page).toHaveURL(ORG_ADMIN_URL_RE)
  })

  test('should show error with wrong password', async ({ page }) => {
    await page.goto('/login')
    await page.getByLabel('Username').fill('admin')
    await page.getByLabel('Password').fill('wrongpassword')
    await page.getByRole('button', { name: 'Sign in' }).click()

    // Should remain on the login page with an error
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()
    // Should NOT redirect to a workspace page (login failed)
    await expect(page).not.toHaveURL(ORG_URL_RE)
    // Verify an error message is displayed (not just URL check)
    await expect(page.getByText(INVALID_CREDENTIALS_RE)).toBeVisible()
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
    const targetPath = '/o/admin'
    await page.goto(targetPath)

    // Should redirect to login with redirect query param
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()
    expect(page.url()).toContain('redirect=')

    // Login
    await page.getByLabel('Username').fill('admin')
    await page.getByLabel('Password').fill('admin123')
    await page.getByRole('button', { name: 'Sign in' }).click()

    // Should redirect back to the original page
    await expect(page).toHaveURL(ORG_ADMIN_URL_RE)
  })
})
