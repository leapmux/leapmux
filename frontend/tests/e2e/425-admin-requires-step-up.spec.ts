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
  test('shows the unlock entry without a prompt after dropping an administrator\'s window', async ({ page, leapmuxServer }) => {
    const cookie = await freshAdminSessionViaAPI(leapmuxServer.hubUrl)
    await loginViaToken(page, cookie)
    await page.goto(`${leapmuxServer.hubUrl}/?prefs=admin`)

    await expect(page.getByTestId('preferences-nav-unlock-admin')).toBeVisible()
    await expect(page.getByText('Verify your identity')).not.toBeVisible()
  })

  test('reveals admin settings on unlock until verification ends', async ({ page, leapmuxServer }) => {
    const cookie = await freshAdminSessionViaAPI(leapmuxServer.hubUrl)
    await loginViaToken(page, cookie)
    const requestedSection = new URL('/?prefs=admin-general', leapmuxServer.hubUrl).href
    await page.goto(requestedSection)

    const general = page.getByTestId('preferences-nav-admin-general')
    const unlock = page.getByTestId('preferences-nav-unlock-admin')
    await unlock.click()
    await expect(page.getByText('Verify your identity')).toBeVisible()

    await answerElevationPrompt(page, TEST_ADMIN_PASSWORD)

    await expect(general).toHaveAttribute('aria-selected', 'true')
    await page.getByTestId('elevation-drop').click()
    await expect(general).not.toBeVisible()
    await expect(unlock).toBeVisible()
    await expect(page.getByTestId('preferences-nav-admin-apps')).toBeVisible()
    await expect(page).toHaveURL(requestedSection)
  })

  test('shows admin groups immediately after an administrator signs in through the page', async ({ page, leapmuxServer }) => {
    await page.goto(`${leapmuxServer.hubUrl}/login`)
    await page.getByLabel(/username/i).fill(TEST_ADMIN_USERNAME)
    await page.getByLabel(/password/i).fill(TEST_ADMIN_PASSWORD)
    await page.getByRole('button', { name: /sign in/i }).click()

    await expect(page).toHaveURL(APP_HOME_URL_RE)
    await openSettingsAt(page, 'admin-general')

    await expect(page.getByTestId('preferences-nav-admin-general')).toBeVisible()
    await expect(page.getByRole('dialog', { name: 'Verify your identity' })).not.toBeVisible()
  })
})
