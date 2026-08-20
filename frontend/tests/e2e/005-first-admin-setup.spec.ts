import type { UnseededDevServerHandle } from './helpers/devServer'
import { test as base, expect } from '@playwright/test'
import { getCurrentUser } from './helpers/api'
import { startUnseededDevServer, stopDevServer } from './helpers/devServer'
import { getBrowserPrefValue, openPreferencesDialog, pickTheme, solveCaptchaViaUI } from './helpers/ui'

/**
 * Uses a standalone unseeded dev server (no pre-registered admin) so we can
 * exercise the /setup flow. Scoped per test so each test sees a fresh
 * setup-mode instance; the shared fixtures from fixtures.ts can't be used
 * because that fixture signs up `admin` automatically.
 */
// Playwright fixtures declare their dependencies by destructuring the first
// parameter; this fixture has no dependencies, hence the empty pattern.
// eslint-disable-next-line no-empty-pattern
async function setupServer({}: object, use: (server: UnseededDevServerHandle) => Promise<void>): Promise<void> {
  const server = await startUnseededDevServer({ dataDirPrefix: 'leapmux-e2e-setup' })
  try {
    await use(server)
  }
  finally {
    await stopDevServer(server)
  }
}

const test = base.extend<{ server: UnseededDevServerHandle }>({
  server: setupServer,
  baseURL: async ({ server }, use) => {
    await use(server.hubUrl)
  },
})

test.describe('First-admin setup', () => {
  test('root path redirects to /setup on a fresh instance', async ({ page }) => {
    await page.goto('/')
    await expect(page).toHaveURL(/\/setup$/)
    await expect(page.getByRole('heading', { name: /Welcome to LeapMux/i })).toBeVisible()
  })

  test('setup rejects reserved username "solo"', async ({ page }) => {
    await page.goto('/setup')
    await page.getByLabel('Username').fill('solo')
    await page.getByLabel('Display Name').fill('Solo')
    await page.getByLabel('New Password').fill('strongpass1')
    await page.getByLabel('Confirm Password').fill('strongpass1')
    await solveCaptchaViaUI(page)
    await page.getByRole('button', { name: 'Create account' }).click()
    await expect(page.getByText(/reserved username/i)).toBeVisible()
    await expect(page).toHaveURL(/\/setup$/)
  })

  test('setup accepts username "admin" and marks the user as admin', async ({ page, server, context }) => {
    await page.goto('/setup')
    await page.getByLabel('Username').fill('admin')
    await page.getByLabel('Display Name').fill('Admin')
    await page.getByLabel('New Password').fill('strongpass1')
    await page.getByLabel('Confirm Password').fill('strongpass1')
    await solveCaptchaViaUI(page)
    await page.getByRole('button', { name: 'Create account' }).click()
    // Flat home route: post-setup lands on `/` (the authenticated home),
    // not an org-scoped path. Matches APP_HOME_URL_RE in the auth specs.
    await expect(page).toHaveURL(/\/$/)

    // Verify the backend recorded this user as an admin.
    const cookies = await context.cookies()
    const session = cookies.find(c => c.name === 'leapmux-session')
    expect(session?.value).toBeTruthy()
    const user = await getCurrentUser(server.hubUrl, `leapmux-session=${session!.value}`)
    expect(user.isAdmin).toBe(true)
    expect(user.username).toBe('admin')
  })

  // The first screen a hub or dev-mode install ever shows. A theme picked here
  // has to survive signing up -- that is the whole point of offering it before
  // there is an account.
  test('the theme picked during setup is the one Preferences shows afterwards', async ({ page }) => {
    await page.goto('/setup')
    await expect(page.getByTestId('theme-chooser')).toBeVisible()

    await pickTheme(page.locator('body'), 'nord')
    await page.getByRole('radiogroup', { name: 'Theme mode' }).getByRole('radio', { name: 'Dark' }).click()

    // It applies to the page it was chosen on, not only after a reload.
    await expect(page.locator('html')).toHaveAttribute('data-ui-theme', 'nord')
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')

    await page.getByLabel('Username').fill('admin')
    await page.getByLabel('Display Name').fill('Admin')
    await page.getByLabel('New Password').fill('strongpass1')
    await page.getByLabel('Confirm Password').fill('strongpass1')
    await solveCaptchaViaUI(page)
    await page.getByRole('button', { name: 'Create account' }).click()
    await expect(page).toHaveURL(/\/$/)

    // Still applied after the navigation into the app...
    await expect(page.locator('html')).toHaveAttribute('data-ui-theme', 'nord')

    // ...and it is the ordinary `theme` preference, so the dialog shows it.
    await expect.poll(() => getBrowserPrefValue(page, 'theme')).toEqual({ name: 'nord', mode: 'dark' })
    await openPreferencesDialog(page, 'appearance')
    const dialog = page.getByRole('dialog', { name: 'Preferences' })
    const themeRow = dialog.locator('[data-setting-id="appearance.theme"]')
    await expect(themeRow.getByTestId('theme-chooser-name')).toHaveAttribute('data-value', 'nord')
    await expect(
      themeRow.getByRole('radiogroup', { name: 'Theme mode' }).getByRole('radio', { name: 'Dark' }),
    ).toHaveAttribute('aria-checked', 'true')
  })
})
