import { expect, test } from './fixtures'
import { deleteStorageDatabase } from './helpers/storage'
import { loginViaUI, logoutViaUI } from './helpers/ui'

// Component tests cover empty fields and authentication redirects.
// Captcha helper unit tests cover readiness failures without a two-minute browser timeout.
test.describe('authentication storage independence', () => {
  test('keeps an HttpOnly session after browser storage is cleared', async ({ page }) => {
    await loginViaUI(page)
    await expect(page).toHaveURL(/\/$/)
    const session = (await page.context().cookies()).find(cookie => cookie.name === 'leapmux-session')
    expect(session).toBeDefined()
    expect(session!.httpOnly).toBe(true)

    // Simulate the browser's data deletion outside the app's storage gateway.
    await page.evaluate(() => {
      localStorage.clear()
      sessionStorage.clear()
    })
    await deleteStorageDatabase(page)
    await page.reload()
    await expect(page).toHaveURL(/\/$/)
    await expect(page).not.toHaveURL(/\/login/)

    await logoutViaUI(page)
    await page.goto('/')
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()
  })
})
