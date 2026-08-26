import { expect, test } from './fixtures'
import { deletePasskeyResponse, deletePasskeyViaAPI, elevateSessionViaAPI, ELEVATION_REQUIRED_HEADER, listPasskeysViaAPI, loginViaAPI, signUpViaAPI, TEST_ADMIN_PASSWORD, TEST_ADMIN_USERNAME } from './helpers/api'
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
    await loginViaToken(page, cookie)
    await page.goto('/')
    await expect(page).toHaveURL(APP_HOME_URL_RE)

    await logoutViaUI(page)
    const loginCookie = await loginWithPasskeyViaAPIInBrowser(page, leapmuxServer.hubUrl, username)
    await loginViaToken(page, loginCookie)
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

    await loginViaToken(page, cookie)
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

    await loginViaToken(page, cookie)
    await page.goto('/')
    await addPasskeyViaAPIInBrowser(page, leapmuxServer.hubUrl, cookie, password)

    await logoutViaUI(page)
    await loginViaUI(page, username, password)
    await expect(page).toHaveURL(APP_HOME_URL_RE)
  })

  test('removes a passkey from a password account once the session is elevated', async ({ page, leapmuxServer }) => {
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
    await loginViaToken(page, cookie)
    await page.goto('/')
    await addPasskeyViaAPIInBrowser(page, leapmuxServer.hubUrl, cookie, password)

    const before = await listPasskeysViaAPI(leapmuxServer.hubUrl, cookie)
    expect(before.length).toBeGreaterThan(0)

    // The add above already elevated this session, and the window covers
    // this second action -- one proven factor, several sensitive actions.
    await deletePasskeyViaAPI(leapmuxServer.hubUrl, cookie, before[0]!.id)
    expect(await listPasskeysViaAPI(leapmuxServer.hubUrl, cookie)).toHaveLength(0)
  })

  test('refuses passkey management on a session that did not prove a factor', async ({ page, leapmuxServer }) => {
    await enableVirtualAuthenticator(page)

    const username = `unelevated-${Date.now()}`
    const password = 'password123'
    const cookie = await signUpViaAPI(
      leapmuxServer.hubUrl,
      username,
      password,
      'Unelevated',
      `${username}@test.local`,
    )
    await loginViaToken(page, cookie)
    await page.goto('/')
    await addPasskeyViaAPIInBrowser(page, leapmuxServer.hubUrl, cookie, password)
    const before = await listPasskeysViaAPI(leapmuxServer.hubUrl, cookie)
    expect(before.length).toBeGreaterThan(0)

    // A SECOND session for the same account: freshly signed in, and so
    // never elevated. It must not be able to delete the passkey.
    const freshCookie = await loginViaAPI(leapmuxServer.hubUrl, username, password)
    const refusal = await deletePasskeyResponse(leapmuxServer.hubUrl, freshCookie, before[0]!.id)
    expect(refusal.ok).toBe(false)
    // The MARKER, not the status. Both refusals the hub can give here are
    // FailedPrecondition -- this retryable one, and the permanent "this
    // credential can never elevate" answer a bearer gets -- so a status
    // assertion passes against either, including the wrong one.
    expect(refusal.headers.get(ELEVATION_REQUIRED_HEADER)).toBe('1')
    expect(await listPasskeysViaAPI(leapmuxServer.hubUrl, cookie)).toHaveLength(before.length)

    // Elevated, the same call lands.
    await elevateSessionViaAPI(leapmuxServer.hubUrl, freshCookie, password)
    await deletePasskeyViaAPI(leapmuxServer.hubUrl, freshCookie, before[0]!.id)
    expect(await listPasskeysViaAPI(leapmuxServer.hubUrl, cookie)).toHaveLength(before.length - 1)
  })

  test('deactivates passkey-only auth with a passkey step-up and a new password', async ({ page, leapmuxServer }) => {
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

  test('asks for a factor once, then covers the next action in the window', async ({ page, leapmuxServer }) => {
    await enableVirtualAuthenticator(page)
    // A FRESH admin session, never the worker-scoped `adminToken`.
    //
    // An elevation lasts two hours and that shared cookie outlives this file,
    // so a test that reuses it both depends on nobody having elevated it
    // earlier and leaves it elevated -- with a new passkey on the account --
    // for every spec that runs after. 009-elevation.spec.ts and
    // 143-cli-elevation.spec.ts both mint their own for the same reason.
    const cookie = await loginViaAPI(leapmuxServer.hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD)
    await loginViaToken(page, cookie)
    await page.goto('/')
    await expect(page).toHaveURL(APP_HOME_URL_RE)

    const prefs = await openAccountSettings(page)

    // The prompt comes FIRST, on the click, and the add dialog opens only
    // after the factor is proven: the two credential prompts then arrive in
    // the order a person expects, and neither modal sits under the other.
    await prefs.getByRole('button', { name: 'Add passkey' }).click()
    const verifyDialog = page.getByRole('dialog', { name: 'Verify your identity' })
    await expect(verifyDialog).toBeVisible()
    await expect(page.getByRole('dialog', { name: 'Add passkey' })).toHaveCount(0)
    await verifyDialog.getByTestId('elevate-password').fill(TEST_ADMIN_PASSWORD)
    await verifyDialog.getByTestId('elevate-password-submit').click()

    // The add dialog carries NO secret: the step-up belongs to the session.
    const addDialog = page.getByRole('dialog', { name: 'Add passkey' })
    await expect(addDialog).toBeVisible()
    await expect(addDialog.getByLabel('Current password')).toHaveCount(0)
    await expect(addDialog.getByRole('button', { name: 'Continue' })).toBeEnabled()
    await addDialog.getByRole('button', { name: 'Continue' }).click()

    // The registration ran with no second prompt.
    await expect(prefs.getByText('Passkey added.')).toBeVisible()

    // A SECOND sensitive action in the same window: no second prompt.
    await prefs.getByRole('button', { name: 'Rename' }).first().click()
    // `exact`, because the profile section's "Save Profile" also matches a
    // substring name and is disabled while the profile is unchanged.
    await prefs.getByRole('button', { name: 'Save', exact: true }).first().click()
    await expect(prefs.getByText('Passkey renamed.')).toBeVisible()
    await expect(page.getByRole('dialog', { name: 'Verify your identity' })).toHaveCount(0)
  })
})
