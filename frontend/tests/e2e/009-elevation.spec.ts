/**
 * Session elevation ("sudo mode") through the real UI.
 *
 * The property under test is the ONE the window exists for and a single-use
 * proof did not have: prove a factor once, and every sensitive action for
 * the next two hours succeeds without another prompt. The unit tests pin the
 * predicate and the SQL clamp; only a browser can show that the prompt
 * appears where a user meets it and does not appear again.
 *
 * Every test signs up its OWN account. An elevation lasts two hours and the
 * fixture's admin session outlives the file, so sharing it would make each
 * test pass or fail on whether an earlier one happened to elevate -- and a
 * password change on the shared admin would break every later spec.
 */

import { expect, test } from './fixtures'
import { elevateSessionViaAPI, listMyAPITokensViaAPI, signUpViaAPI } from './helpers/api'
import { loginViaToken, openAccountSettings } from './helpers/ui'

const APP_HOME_URL_RE = /\/$/
const PASSWORD = 'password123'

/** A fresh account plus its session cookie. */
async function freshAccount(hubUrl: string, prefix: string): Promise<string> {
  const username = `${prefix}-${Date.now()}-${Math.floor(Math.random() * 100000)}`
  return signUpViaAPI(hubUrl, username, PASSWORD, 'Elevation User', `${username}@test.local`)
}

test.describe('session elevation', () => {
  test('prompts once, then covers a second sensitive action', async ({ page, leapmuxServer }) => {
    const cookie = await freshAccount(leapmuxServer.hubUrl, 'elev-once')
    await loginViaToken(page, cookie)
    await page.goto('/')
    await expect(page).toHaveURL(APP_HOME_URL_RE)

    const prefs = await openAccountSettings(page)
    await prefs.getByLabel('New Password').fill('elevated-pass-1')
    await prefs.getByLabel('Confirm Password').fill('elevated-pass-1')
    await prefs.getByRole('button', { name: 'Change Password' }).click()

    // The hub refuses the un-elevated session and the client prompts. The
    // password field belongs to the PROMPT: the change-password form itself
    // no longer carries a current-password field at all.
    const verify = page.getByRole('dialog', { name: 'Verify your identity' })
    await expect(verify).toBeVisible()
    await expect(prefs.getByLabel('Current Password')).toHaveCount(0)
    await verify.getByTestId('elevate-password').fill(PASSWORD)
    await verify.getByTestId('elevate-password-submit').click()

    await expect(prefs.getByText('Password changed.')).toBeVisible()

    // A SECOND sensitive action in the same window: no second prompt.
    await prefs.getByLabel('New Password').fill('elevated-pass-2')
    await prefs.getByLabel('Confirm Password').fill('elevated-pass-2')
    await prefs.getByRole('button', { name: 'Change Password' }).click()
    await expect(prefs.getByText('Password changed.')).toBeVisible()
    await expect(page.getByRole('dialog', { name: 'Verify your identity' })).toHaveCount(0)
  })

  test('reports the wrong password without granting the window', async ({ page, leapmuxServer }) => {
    const cookie = await freshAccount(leapmuxServer.hubUrl, 'elev-wrong')
    await loginViaToken(page, cookie)
    await page.goto('/')

    const prefs = await openAccountSettings(page)
    await prefs.getByLabel('New Password').fill('should-not-apply')
    await prefs.getByLabel('Confirm Password').fill('should-not-apply')
    await prefs.getByRole('button', { name: 'Change Password' }).click()

    const verify = page.getByRole('dialog', { name: 'Verify your identity' })
    await verify.getByTestId('elevate-password').fill('definitely-not-the-password')
    await verify.getByTestId('elevate-password-submit').click()

    // The prompt stays open with the refusal, and the queued action did not
    // run: a wrong factor must not admit the change it restricted.
    await expect(verify.getByRole('alert')).toBeVisible()
    await expect(verify).toBeVisible()
    await expect(prefs.getByText('Password changed.')).toHaveCount(0)
  })

  test('lists the connected apps without a prompt', async ({ page, leapmuxServer }) => {
    // Listing and disconnecting deliberately do NOT require elevation:
    // disconnecting only reduces access, and waiting is the attacker's gain.
    // The rule gets STRONGER with third-party apps, not weaker.
    const cookie = await freshAccount(leapmuxServer.hubUrl, 'elev-devices')
    expect(await listMyAPITokensViaAPI(leapmuxServer.hubUrl, cookie)).toEqual([])

    await loginViaToken(page, cookie)
    await page.goto('/')
    const prefs = await openAccountSettings(page)
    await expect(prefs.getByTestId('connected-apps')).toBeVisible()
    await expect(prefs.getByText('No connected apps.', { exact: false })).toBeVisible()
    await expect(page.getByRole('dialog', { name: 'Verify your identity' })).toHaveCount(0)
  })

  // The window is ONE window across every sensitive action, not one per
  // action type. Two different kinds of change, one immediately after the
  // other, is the case that proves it: a per-action secret would ask twice.
  test('covers a second action of a different kind', async ({ page, leapmuxServer }) => {
    const cookie = await freshAccount(leapmuxServer.hubUrl, 'elev-cross')
    await loginViaToken(page, cookie)
    await page.goto('/')
    await expect(page).toHaveURL(APP_HOME_URL_RE)

    const prefs = await openAccountSettings(page)
    await prefs.getByLabel('New Email').fill('moved@test.local')
    await prefs.getByRole('button', { name: 'Change Email' }).click()

    // Moving the account email requires elevation: it receives the password-reset
    // link, so a session alone must not move it.
    const verify = page.getByRole('dialog', { name: 'Verify your identity' })
    await expect(verify).toBeVisible()
    await verify.getByTestId('elevate-password').fill(PASSWORD)
    await verify.getByTestId('elevate-password-submit').click()

    // Either branch is a success: the hub applies the change immediately or
    // sends a verification message, depending on the deployment's settings.
    await expect(prefs.getByText(/Email updated\.|Verification email sent\./)).toBeVisible()

    // A DIFFERENT kind of sensitive action, inside the same window.
    await prefs.getByLabel('New Password').fill('elevated-pass-x')
    await prefs.getByLabel('Confirm Password').fill('elevated-pass-x')
    await prefs.getByRole('button', { name: 'Change Password' }).click()
    await expect(prefs.getByText('Password changed.')).toBeVisible()
    await expect(page.getByRole('dialog', { name: 'Verify your identity' })).toHaveCount(0)
  })

  test('never prompts an already-elevated session', async ({ page, leapmuxServer }) => {
    const cookie = await freshAccount(leapmuxServer.hubUrl, 'elev-pre')
    // Elevate out of band, exactly as a prior action in the same window
    // would have.
    await elevateSessionViaAPI(leapmuxServer.hubUrl, cookie, PASSWORD)

    await loginViaToken(page, cookie)
    await page.goto('/')
    const prefs = await openAccountSettings(page)
    await prefs.getByLabel('New Password').fill('rotated-pass-1')
    await prefs.getByLabel('Confirm Password').fill('rotated-pass-1')
    await prefs.getByRole('button', { name: 'Change Password' }).click()

    await expect(prefs.getByText('Password changed.')).toBeVisible()
    await expect(page.getByRole('dialog', { name: 'Verify your identity' })).toHaveCount(0)
  })
})

/**
 * The verified-session state, and the control that ends it.
 *
 * It sits at the TOP of every group whose rows the hub refuses without a
 * proven factor -- above them, because it is the thing that makes those rows
 * succeed without a prompt. It used to sit inside one account editor, half
 * way down the panel and under a Save button that was unrelated to it.
 */
test.describe('the verified-session state', () => {
  test('appears above the account rows once the user proves a factor', async ({ page, leapmuxServer }) => {
    const cookie = await freshAccount(leapmuxServer.hubUrl, 'elev-panel')
    await loginViaToken(page, cookie)
    await page.goto('/')
    await expect(page).toHaveURL(APP_HOME_URL_RE)

    const prefs = await openAccountSettings(page)
    // Nothing proven yet.
    await expect(prefs.getByTestId('elevation-status')).toHaveCount(0)

    await prefs.getByLabel('New Password').fill('elevated-pass-panel')
    await prefs.getByLabel('Confirm Password').fill('elevated-pass-panel')
    await prefs.getByRole('button', { name: 'Change Password' }).click()

    const verify = page.getByRole('dialog', { name: 'Verify your identity' })
    await verify.getByTestId('elevate-password').fill(PASSWORD)
    await verify.getByTestId('elevate-password-submit').click()
    await expect(prefs.getByText('Password changed.')).toBeVisible()

    const status = prefs.getByTestId('elevation-status')
    await expect(status).toBeVisible()
    await expect(status).toContainText('This session is verified')

    // ABOVE the first account row, not somewhere inside the panel. The test
    // compares by document position, because "is visible" was true of the old
    // placement too.
    const panel = page.locator('#preferences-panel')
    const order = await panel.evaluate((el) => {
      const status = el.querySelector('[data-testid="elevation-status"]')
      const firstRow = el.querySelector('[data-setting-id]')
      if (!status || !firstRow)
        return 'missing'
      // Node.DOCUMENT_POSITION_FOLLOWING === 4.
      return (status.compareDocumentPosition(firstRow) & 4) !== 0 ? 'before' : 'after'
    })
    expect(order).toBe('before')
  })

  test('ends the window on demand', async ({ page, leapmuxServer }) => {
    const cookie = await freshAccount(leapmuxServer.hubUrl, 'elev-drop')
    await elevateSessionViaAPI(leapmuxServer.hubUrl, cookie, PASSWORD)
    await loginViaToken(page, cookie)
    await page.goto('/')

    const prefs = await openAccountSettings(page)
    await expect(prefs.getByTestId('elevation-status')).toBeVisible()
    await prefs.getByTestId('elevation-drop').click()
    await expect(prefs.getByTestId('elevation-status')).toHaveCount(0)

    // And the next sensitive action asks again, which is the whole point of
    // the control.
    await prefs.getByLabel('New Password').fill('after-drop-pass')
    await prefs.getByLabel('Confirm Password').fill('after-drop-pass')
    await prefs.getByRole('button', { name: 'Change Password' }).click()
    await expect(page.getByRole('dialog', { name: 'Verify your identity' })).toBeVisible()
  })
})
