import { join } from 'node:path'
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
import { signUpViaUI, solveCaptchaViaUI } from './helpers/ui'

function hubDataDir(dataDir: string): string {
  return join(dataDir, 'hub')
}

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
      await expect(page.getByTestId('verify-email-resend-status')).toContainText(/fresh code has been sent/i)
      await expect(page.getByTestId('verify-email-resend')).toBeDisabled()
      await expect(page.getByTestId('verify-email-resend')).toContainText(/Resend code \(\d+:\d{2}\)/)
    })
  })

  test('unverified users can still log out while verification is pending', async ({ page, leapmuxServer }) => {
    await withCaptureSmtp(leapmuxServer, async () => {
      const username = `unverified-${Date.now()}`
      await signUpViaUI(page, username, 'password123', 'Unverified', `${username}@test.local`)
      await expect(page).toHaveURL(/\/verify-email/)

      const cookie = await (async () => {
        const session = (await page.context().cookies()).find(c => c.name === 'leapmux-session')
        if (!session)
          throw new Error('expected session cookie on verify-email page')
        return `leapmux-session=${session.value}`
      })()
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

  test('enabling SMTP later gates previously unverified users', async ({ page, leapmuxServer }) => {
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
