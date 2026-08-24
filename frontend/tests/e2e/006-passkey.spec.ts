import { expect, test } from './fixtures'
import { deletePasskeyViaAPI, listPasskeysViaAPI, signUpViaAPI, TEST_ADMIN_PASSWORD } from './helpers/api'
import {
  loginViaToken,
  loginViaUI,
  loginWithPasskeyViaUI,
  logoutViaUI,
  openAccountSettings,
  signUpWithPasskeyViaUI,
} from './helpers/ui'
import {
  addPasskeyViaAPIInBrowser,
  deactivatePasskeyAuthViaAPIInBrowser,
  enableVirtualAuthenticator,
  loginWithPasskeyViaAPIInBrowser,
  signUpWithPasskeyViaAPIInBrowser,
} from './helpers/webauthn'

const APP_HOME_URL_RE = /\/$/

async function setSessionCookie(page: import('@playwright/test').Page, cookie: string) {
  const value = cookie.split('=').slice(1).join('=')
  await page.context().addCookies([{
    name: 'leapmux-session',
    value,
    domain: 'localhost',
    path: '/',
    httpOnly: true,
  }])
}

test.describe('Passkey authentication', () => {
  test('signs up with a passkey via the signup form and logs in again', async ({ page, leapmuxServer }) => {
    await enableVirtualAuthenticator(page)

    const username = `passkey-${Date.now()}`
    const email = `${username}@test.local`
    await signUpWithPasskeyViaUI(page, username, email, 'Passkey User')
    await expect(page).toHaveURL(APP_HOME_URL_RE)

    await logoutViaUI(page)
    await loginWithPasskeyViaUI(page, username)
    await expect(page).toHaveURL(APP_HOME_URL_RE)
  })

  test('signs up with a passkey through the API and logs in with passkey', async ({ page, leapmuxServer }) => {
    await enableVirtualAuthenticator(page)

    const username = `passkey-api-${Date.now()}`
    const email = `${username}@test.local`
    const cookie = await signUpWithPasskeyViaAPIInBrowser(page, leapmuxServer.hubUrl, username, email, 'Passkey API')
    await setSessionCookie(page, cookie)
    await page.goto('/')
    await expect(page).toHaveURL(APP_HOME_URL_RE)

    await logoutViaUI(page)
    const loginCookie = await loginWithPasskeyViaAPIInBrowser(page, leapmuxServer.hubUrl, username)
    await setSessionCookie(page, loginCookie)
    await page.goto('/')
    await expect(page).toHaveURL(APP_HOME_URL_RE)
  })

  test('adds a passkey to a password account and signs in with it', async ({ page, leapmuxServer }) => {
    await enableVirtualAuthenticator(page)

    const username = `pw-passkey-${Date.now()}`
    const password = 'password123'
    const cookie = await signUpViaAPI(
      leapmuxServer.hubUrl,
      username,
      password,
      'Password User',
      `${username}@test.local`,
    )

    await setSessionCookie(page, cookie)
    await page.goto('/')
    await expect(page).toHaveURL(APP_HOME_URL_RE)

    await addPasskeyViaAPIInBrowser(page, leapmuxServer.hubUrl, cookie, password)
    expect((await listPasskeysViaAPI(leapmuxServer.hubUrl, cookie)).length).toBeGreaterThan(0)

    await logoutViaUI(page)
    await loginWithPasskeyViaUI(page, username)
    await expect(page).toHaveURL(APP_HOME_URL_RE)
  })

  test('password login still works when both methods are registered', async ({ page, leapmuxServer }) => {
    await enableVirtualAuthenticator(page)

    const username = `dual-${Date.now()}`
    const password = 'password123'
    const cookie = await signUpViaAPI(
      leapmuxServer.hubUrl,
      username,
      password,
      'Dual Login',
      `${username}@test.local`,
    )

    await setSessionCookie(page, cookie)
    await page.goto('/')
    await addPasskeyViaAPIInBrowser(page, leapmuxServer.hubUrl, cookie, password)

    await logoutViaUI(page)
    await loginViaUI(page, username, password)
    await expect(page).toHaveURL(APP_HOME_URL_RE)
  })

  test('removes a passkey from a password account with the current password', async ({ page, leapmuxServer }) => {
    await enableVirtualAuthenticator(page)

    const username = `rm-passkey-${Date.now()}`
    const password = 'password123'
    const cookie = await signUpViaAPI(
      leapmuxServer.hubUrl,
      username,
      password,
      'Remove Passkey',
      `${username}@test.local`,
    )
    await setSessionCookie(page, cookie)
    await page.goto('/')
    await addPasskeyViaAPIInBrowser(page, leapmuxServer.hubUrl, cookie, password)

    const before = await listPasskeysViaAPI(leapmuxServer.hubUrl, cookie)
    expect(before.length).toBeGreaterThan(0)

    await deletePasskeyViaAPI(leapmuxServer.hubUrl, cookie, before[0]!.id, password)
    expect(await listPasskeysViaAPI(leapmuxServer.hubUrl, cookie)).toHaveLength(0)
  })

  test('deactivates passkey-only auth with reauth and a new password', async ({ page, leapmuxServer }) => {
    await enableVirtualAuthenticator(page)

    const username = `deact-passkey-${Date.now()}`
    const email = `${username}@test.local`
    const newPassword = 'password123'
    const cookie = await signUpWithPasskeyViaAPIInBrowser(page, leapmuxServer.hubUrl, username, email, 'Deact User')
    expect((await listPasskeysViaAPI(leapmuxServer.hubUrl, cookie)).length).toBeGreaterThan(0)

    await deactivatePasskeyAuthViaAPIInBrowser(page, leapmuxServer.hubUrl, cookie, newPassword)
    expect(await listPasskeysViaAPI(leapmuxServer.hubUrl, cookie)).toHaveLength(0)

    await logoutViaUI(page)
    await loginViaUI(page, username, newPassword)
    await expect(page).toHaveURL(APP_HOME_URL_RE)
  })

  test('keeps add-passkey continue disabled until the current password is filled', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    await expect(page).toHaveURL(APP_HOME_URL_RE)

    const prefs = await openAccountSettings(page)
    await expect(prefs.getByRole('button', { name: 'Add passkey' })).toBeEnabled()
    await prefs.getByRole('button', { name: 'Add passkey' }).click()

    const addDialog = page.getByRole('dialog', { name: 'Add passkey' })
    await expect(addDialog).toBeVisible()
    await expect(addDialog.getByRole('button', { name: 'Continue' })).toBeDisabled()
    await addDialog.getByLabel('Current password').fill(TEST_ADMIN_PASSWORD)
    await expect(addDialog.getByRole('button', { name: 'Continue' })).toBeEnabled()
  })
})
