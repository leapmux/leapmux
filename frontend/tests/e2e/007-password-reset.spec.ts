import { join } from 'node:path'
import { expect, test } from './fixtures'
import {
  authedHeaders,
  listPasskeysViaAPI,
  loginViaAPI,
  readPendingEmailToken,
  verifyEmailViaAPI,
} from './helpers/api'
import { extractPasswordResetToken, withCaptureSmtp } from './helpers/mail'
import { loginViaUI, logoutViaUI, solveCaptchaViaUI } from './helpers/ui'
import { enableVirtualAuthenticator, loginWithPasskeyViaAPIInBrowser, signUpWithPasskeyViaAPIInBrowser } from './helpers/webauthn'

function hubDataDir(dataDir: string): string {
  return join(dataDir, 'hub')
}

test.describe('Password reset', () => {
  test('forgot-password flow clears passkeys on completion', async ({ page, leapmuxServer }) => {
    await withCaptureSmtp(leapmuxServer, async (smtp) => {
      await enableVirtualAuthenticator(page)

      const username = `reset-${Date.now()}`
      const email = `${username}@test.local`
      const passkeyCookie = await signUpWithPasskeyViaAPIInBrowser(page, leapmuxServer.hubUrl, username, email)
      const verifyToken = await readPendingEmailToken(hubDataDir(leapmuxServer.dataDir), username)
      await verifyEmailViaAPI(leapmuxServer.hubUrl, passkeyCookie, verifyToken)
      const verifiedCookie = await loginWithPasskeyViaAPIInBrowser(page, leapmuxServer.hubUrl, username)
      await page.context().addCookies([{
        name: 'leapmux-session',
        value: verifiedCookie.split('=').slice(1).join('='),
        domain: 'localhost',
        path: '/',
        httpOnly: true,
      }])
      await page.goto('/')
      await expect(page).toHaveURL(/\/$/)
      expect((await listPasskeysViaAPI(leapmuxServer.hubUrl, verifiedCookie)).length).toBeGreaterThan(0)

      await logoutViaUI(page)

      await page.goto('/forgot-password')
      await page.getByLabel('Email or username').fill(username)
      await solveCaptchaViaUI(page)
      // Arm the wait BEFORE the click: waitForMessage resolves with the next
      // message that arrives, never an already-buffered one (the signup
      // verification email is still in the buffer).
      const resetEmail = smtp.waitForMessage()
      await page.getByRole('button', { name: 'Send reset link' }).click()
      await expect(page.getByText(/If an account with that email or username exists/i)).toBeVisible()

      const emailBody = await resetEmail
      const token = extractPasswordResetToken(emailBody)

      await page.goto(`/reset-password?token=${encodeURIComponent(token)}`)
      const newPassword = 'newpass123'
      await page.getByLabel('New Password').fill(newPassword)
      await page.getByLabel('Confirm Password').fill(newPassword)
      await solveCaptchaViaUI(page)
      await page.getByRole('button', { name: 'Reset password' }).click()
      await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()

      const newCookie = await loginViaAPI(leapmuxServer.hubUrl, username, newPassword)
      expect(await listPasskeysViaAPI(leapmuxServer.hubUrl, newCookie)).toHaveLength(0)

      await fetch(`${leapmuxServer.hubUrl}/leapmux.v1.AuthService/Logout`, {
        method: 'POST',
        headers: authedHeaders(newCookie),
        body: '{}',
      })

      const captcha = await (async () => {
        const { solveCaptchaViaAPI } = await import('./helpers/altcha')
        return solveCaptchaViaAPI(leapmuxServer.hubUrl)
      })()
      const passkeyBegin = await fetch(`${leapmuxServer.hubUrl}/leapmux.v1.AuthService/BeginPasskeyLogin`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          username,
          captchaPayload: captcha.captchaPayload,
          honeypot: captcha.honeypot,
        }),
      })
      expect(passkeyBegin.ok).toBe(false)
      expect(await passkeyBegin.text()).toMatch(/no passkeys|failed_precondition|not found/i)

      await loginViaUI(page, username, newPassword)
    })
  })

  test('shows the forgot-password link when email is enabled', async ({ page, leapmuxServer }) => {
    await withCaptureSmtp(leapmuxServer, async () => {
      await page.goto('/login')
      await page.getByLabel('Username').fill('admin')
      await page.getByLabel('Username').blur()
      await expect(page.getByRole('link', { name: 'Forgot password?' })).toBeVisible()
    })
  })
})
