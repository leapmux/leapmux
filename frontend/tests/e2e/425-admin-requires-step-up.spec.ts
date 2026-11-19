import { expect } from '@playwright/test'
import { test } from './fixtures'
import {
  freshAdminSessionViaAPI,
  TEST_ADMIN_PASSWORD,
  TEST_ADMIN_USERNAME,
} from './helpers/api'
import { openSettingsAt } from './helpers/ui'

test.describe('425-admin-requires-step-up', () => {
  test('an administrator whose window was dropped sees the unlock entry and no prompt', async ({ page, leapmuxServer }) => {
    const cookie = await freshAdminSessionViaAPI(leapmuxServer.hubUrl)
    await page.context().addCookies([
      {
        name: 'leapmux-session',
        value: cookie.replace('leapmux-session=', ''),
        domain: new URL(leapmuxServer.hubUrl).hostname,
        path: '/',
      },
    ])
    await page.goto(`${leapmuxServer.hubUrl}/?prefs=admin`)

    await expect(page.getByTestId('preferences-nav-unlock-admin')).toBeVisible()
    await expect(page.getByText('Verify your identity')).not.toBeVisible()
  })

  test('selecting the unlock entry opens the prompt and reveals admin groups after unlock', async ({ page, leapmuxServer }) => {
    const cookie = await freshAdminSessionViaAPI(leapmuxServer.hubUrl)
    await page.context().addCookies([
      {
        name: 'leapmux-session',
        value: cookie.replace('leapmux-session=', ''),
        domain: new URL(leapmuxServer.hubUrl).hostname,
        path: '/',
      },
    ])
    await page.goto(`${leapmuxServer.hubUrl}/?prefs=admin`)

    await page.getByTestId('preferences-nav-unlock-admin').click()
    await expect(page.getByText('Verify your identity')).toBeVisible()

    await page.getByLabel(/password/i).fill(TEST_ADMIN_PASSWORD)
    await page.getByRole('button', { name: /verify/i }).click()

    await expect(page.locator('[data-testid^="preferences-nav-"]').first()).toBeVisible()
  })

  test('an administrator who signed in through the page sees admin groups at once', async ({ page, leapmuxServer }) => {
    await page.goto(`${leapmuxServer.hubUrl}/login`)
    await page.getByLabel(/username/i).fill(TEST_ADMIN_USERNAME)
    await page.getByLabel(/password/i).fill(TEST_ADMIN_PASSWORD)
    await page.getByRole('button', { name: /sign in/i }).click()

    await page.waitForURL(/\/$/, { timeout: 30000 })
    await openSettingsAt(page, 'admin-general')

    await expect(page.locator('[data-testid^="preferences-nav-"]').first()).toBeVisible()
  })
})
