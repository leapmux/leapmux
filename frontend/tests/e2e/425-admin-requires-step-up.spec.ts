import { expect } from '@playwright/test'
import { test } from './fixtures'
import {
  freshAdminSessionViaAPI,
  TEST_ADMIN_PASSWORD,
  TEST_ADMIN_USERNAME,
} from './helpers/api'
import { answerElevationPrompt, loginViaToken, openSettingsAt } from './helpers/ui'

const APP_HOME_URL_RE = /\/$/

test.describe('425-admin-requires-step-up', () => {
  test('an administrator whose window was dropped sees the unlock entry and no prompt', async ({ page, leapmuxServer }) => {
    const cookie = await freshAdminSessionViaAPI(leapmuxServer.hubUrl)
    await loginViaToken(page, cookie)
    await page.goto(`${leapmuxServer.hubUrl}/?prefs=admin`)

    await expect(page.getByTestId('preferences-nav-unlock-admin')).toBeVisible()
    await expect(page.getByText('Verify your identity')).not.toBeVisible()
  })

  test('selecting the unlock entry opens the prompt and reveals admin groups after unlock', async ({ page, leapmuxServer }) => {
    const cookie = await freshAdminSessionViaAPI(leapmuxServer.hubUrl)
    await loginViaToken(page, cookie)
    await page.goto(`${leapmuxServer.hubUrl}/?prefs=admin`)

    await page.getByTestId('preferences-nav-unlock-admin').click()
    await expect(page.getByText('Verify your identity')).toBeVisible()

    await answerElevationPrompt(page, TEST_ADMIN_PASSWORD)

    await expect(page.getByTestId('preferences-nav-admin-general')).toBeVisible()
  })

  test('an administrator who signed in through the page sees admin groups at once', async ({ page, leapmuxServer }) => {
    await page.goto(`${leapmuxServer.hubUrl}/login`)
    await page.getByLabel(/username/i).fill(TEST_ADMIN_USERNAME)
    await page.getByLabel(/password/i).fill(TEST_ADMIN_PASSWORD)
    await page.getByRole('button', { name: /sign in/i }).click()

    await expect(page).toHaveURL(APP_HOME_URL_RE)
    await openSettingsAt(page, 'admin-general')

    await expect(page.getByTestId('preferences-nav-admin-general')).toBeVisible()
  })
})
