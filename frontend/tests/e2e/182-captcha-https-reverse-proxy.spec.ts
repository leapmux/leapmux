import type { DevServerHandle } from './helpers/devServer'
import type { TlsProxyHandle } from './helpers/tlsProxy'
import { test as base, expect } from '@playwright/test'
import { startDevServer, stopDevServer } from './helpers/devServer'
import { startTlsProxy } from './helpers/tlsProxy'
import { loginViaUI } from './helpers/ui'

/**
 * Pins ALTCHA behind an HTTPS reverse proxy while the Hub itself speaks
 * cleartext HTTP — the documented production shape (TLS terminator → Hub).
 *
 * The secure-context gate keys off the browser Origin / isSecureContext,
 * not the Hub↔proxy hop. Without this spec, a regression that gated on
 * X-Forwarded-Proto or the listen scheme would silently stand down ALTCHA
 * for every TLS-terminated deployment while unit tests (which stash Origin
 * directly) still passed.
 */
const test = base.extend<{ server: DevServerHandle, tlsProxy: TlsProxyHandle }>({
  // eslint-disable-next-line no-empty-pattern
  server: async ({}, use) => {
    const server = await startDevServer({ dataDirPrefix: 'leapmux-e2e-captcha-https-proxy' })
    await use(server)
    await stopDevServer(server)
  },
  tlsProxy: async ({ server }, use) => {
    // Hub stays on http://localhost:<port>; only the browser-facing URL is https.
    expect(server.hubUrl.startsWith('http://localhost:')).toBe(true)
    const proxy = await startTlsProxy(server.hubUrl)
    await use(proxy)
    await proxy.close()
  },
  baseURL: async ({ tlsProxy }, use) => {
    await use(tlsProxy.url)
  },
  context: async ({ browser, baseURL }, use) => {
    const context = await browser.newContext({
      baseURL,
      // Ephemeral self-signed cert minted by startTlsProxy.
      ignoreHTTPSErrors: true,
    })
    await use(context)
    await context.close()
  },
})

test.describe('ALTCHA behind HTTPS reverse proxy', () => {
  test('login solves ALTCHA when the browser is on HTTPS and the Hub is cleartext HTTP', async ({ page, server, tlsProxy }) => {
    expect(tlsProxy.url.startsWith('https://localhost:')).toBe(true)
    expect(server.hubUrl.startsWith('http://localhost:')).toBe(true)

    await page.goto('/login')
    // Ground truth for the gate: the page is a secure context even though
    // the Hub never terminated TLS.
    expect(await page.evaluate(() => ({
      protocol: window.location.protocol,
      secure: window.isSecureContext,
    }))).toEqual({ protocol: 'https:', secure: true })

    // Widget must mount — a false stand-down would leave submit enabled
    // with no altcha-widget (solveCaptchaViaUI would no-op and the login
    // would still "succeed", masking the regression).
    await expect(page.locator('altcha-widget')).toBeVisible()

    await loginViaUI(page)
    await expect(page).toHaveURL(/\/$/)
  })
})
