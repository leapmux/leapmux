import { expect, test } from './fixtures'
import { solveCaptchaViaAPI } from './helpers/altcha'
import {
  authedHeaders,
  backdatePendingEmailIssuedAt,
  clearSmtpViaAPI,
  configureBrokenSmtpViaAPI,
  readPendingEmailToken,
  signUpViaAPI,
  waitForEmailEnabled,
} from './helpers/api'
import { withCaptureSmtp } from './helpers/mail'
import { hubDataDir } from './helpers/server'
import { loginViaToken, openAccountSettings, readSessionCookie, signUpViaUI, solveCaptchaViaUI } from './helpers/ui'

test.describe('Email verification', () => {
  test('signup with SMTP configured routes to verify-email and accepts the code', async ({ page, leapmuxServer }) => {
    await withCaptureSmtp(leapmuxServer, async () => {
      const username = `verify-${Date.now()}`
      await signUpViaUI(page, username, 'password123', 'Verify User', `${username}@test.local`)
      await expect(page).toHaveURL(/\/verify-email/)

      let token = ''
      await expect.poll(async () => {
        token = await readPendingEmailToken(hubDataDir(leapmuxServer.dataDir), username)
        return token
      }).not.toBe('')
      await page.getByTestId('verify-email-code-input').fill(token)
      await page.getByTestId('verify-email-submit').click()
      await expect(page).toHaveURL(/\/$/)
    })
  })

  test('resend code shows a cooldown on the verify-email page', async ({ page, leapmuxServer }) => {
    await withCaptureSmtp(leapmuxServer, async () => {
      const username = `resend-${Date.now()}`
      await signUpViaUI(page, username, 'password123', 'Resend User', `${username}@test.local`)
      await expect(page).toHaveURL(/\/verify-email/)

      // Signup already sent a code; the page seeds the 60s cooldown from
      // the signup response, so the button starts disabled with a countdown
      // instead of round-tripping into a server refusal.
      await expect(page.getByTestId('verify-email-resend')).toBeDisabled()
      await expect(page.getByTestId('verify-email-resend')).toContainText(/Resend code \(\d+:\d{2}\)/)

      // The cooldown seed lives in memory (set by the signup response), so a
      // reload drops it; the backdated row then lets the server accept the
      // resend immediately.
      await backdatePendingEmailIssuedAt(hubDataDir(leapmuxServer.dataDir), username)
      await page.reload()
      await expect(page.getByTestId('verify-email-resend')).toBeEnabled()
      await page.getByTestId('verify-email-resend').click()
      await expect(page.getByTestId('verify-email-resend-status')).toContainText(/sent a fresh code/i)
      await expect(page.getByTestId('verify-email-resend')).toBeDisabled()
      await expect(page.getByTestId('verify-email-resend')).toContainText(/Resend code \(\d+:\d{2}\)/)
    })
  })

  test('unverified users can still log out while verification is pending', async ({ page, leapmuxServer }) => {
    await withCaptureSmtp(leapmuxServer, async () => {
      const username = `unverified-${Date.now()}`
      await signUpViaUI(page, username, 'password123', 'Unverified', `${username}@test.local`)
      await expect(page).toHaveURL(/\/verify-email/)

      const cookie = await readSessionCookie(page, 'sign-up')
      await fetch(`${leapmuxServer.hubUrl}/leapmux.v1.AuthService/Logout`, {
        method: 'POST',
        headers: authedHeaders(cookie),
        body: '{}',
      })
      await page.goto('/login')
      await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()

      await page.getByLabel('Username').fill(username)
      await page.getByLabel('Password').fill('password123')
      await solveCaptchaViaUI(page)
      await page.getByRole('button', { name: 'Sign in' }).click()
      await expect(page).toHaveURL(/\/verify-email/)
    })
  })

  test('signup fails closed when verification email cannot be sent', async ({ page, leapmuxServer }) => {
    await configureBrokenSmtpViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken)
    await waitForEmailEnabled(leapmuxServer.hubUrl)

    const username = `failclosed-${Date.now()}`
    await page.goto('/signup')
    await page.getByLabel('Username').fill(username)
    await page.getByLabel('Display Name').fill('Fail Closed')
    await page.getByLabel('Email').fill(`${username}@test.local`)
    await page.getByLabel('New Password').fill('password123')
    await page.getByLabel('Confirm Password').fill('password123')
    await solveCaptchaViaUI(page)
    await page.getByRole('button', { name: 'Sign up' }).click()
    await expect(page.getByText(/sign-up failed|unavailable|failed/i)).toBeVisible()
    await expect(page).toHaveURL(/\/signup/)

    const captcha = await solveCaptchaViaAPI(leapmuxServer.hubUrl)
    const loginResp = await fetch(`${leapmuxServer.hubUrl}/leapmux.v1.AuthService/Login`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        username,
        password: 'password123',
        captchaPayload: captcha.captchaPayload,
        honeypot: captcha.honeypot,
      }),
    })
    expect(loginResp.ok).toBe(false)
    const loginBody = await loginResp.json() as { code?: string }
    expect(String(loginBody.code ?? '')).toMatch(/unauthenticated/i)
  })

  test('enabling SMTP later restricts previously unverified users', async ({ page, leapmuxServer }) => {
    await clearSmtpViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken)

    const username = `transition-${Date.now()}`
    await signUpViaAPI(
      leapmuxServer.hubUrl,
      username,
      'password123',
      'Transition User',
      `${username}@test.local`,
    )

    await withCaptureSmtp(leapmuxServer, async () => {
      await page.goto('/login')
      await page.getByLabel('Username').fill(username)
      await page.getByLabel('Password').fill('password123')
      await solveCaptchaViaUI(page)
      await page.getByRole('button', { name: 'Sign in' }).click()
      await expect(page).toHaveURL(/\/verify-email/)
    })
  })
})

/**
 * The account panel points an unverified address at `/verify-email` with the
 * router's own `<A>`, and that link renders only on a hub with SMTP
 * configured and an address nobody confirmed.
 *
 * The app mounts its dialogs beside the route outlet. Mounted OUTSIDE the
 * router they had no router context, so `<A>` threw and the whole app went to
 * its error boundary the moment this row appeared -- with the Preferences
 * dialog never opening and a rapid series of "email verification required"
 * toasts as the only clue. The bug needs both halves, which is why nobody
 * noticed it.
 */
test.describe('the account panel on an unverified address', () => {
  test('offers verification without tearing the app down', async ({ page, leapmuxServer }) => {
    await clearSmtpViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken)

    // Signed up BEFORE SMTP: the account becomes verified-by-absence, and the
    // hub starts refusing it only once the relay is configured below.
    const username = `acct-verify-${Date.now()}`
    const cookie = await signUpViaAPI(
      leapmuxServer.hubUrl,
      username,
      'password123',
      'Account Verify',
      `${username}@test.local`,
    )

    await withCaptureSmtp(leapmuxServer, async () => {
      await loginViaToken(page, cookie)
      await page.goto('/')
      const prefs = await openAccountSettings(page)

      await expect(prefs.getByRole('link', { name: 'Enter the code' })).toBeVisible()
      await expect(prefs.getByRole('button', { name: 'Resend code' })).toBeVisible()
      // The app's error boundary, which the crash rendered in place of
      // everything else.
      await expect(page.getByRole('heading', { name: 'Uncaught Error' })).toHaveCount(0)
    })
  })
})
