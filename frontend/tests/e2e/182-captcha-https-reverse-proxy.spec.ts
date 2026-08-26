import type { DevServerHandle } from './helpers/devServer'
import type { TlsProxyHandle } from './helpers/tlsProxy'
import { test as base, expect } from '@playwright/test'
import { mintCLITokenForAdmin, setHubSetting } from './helpers/cli'
import { startDevServer, stopDevServer } from './helpers/devServer'
import { startTlsProxy } from './helpers/tlsProxy'
import { loginViaUI } from './helpers/ui'

/**
 * Pins ALTCHA behind an HTTPS reverse proxy while the Hub itself speaks
 * cleartext HTTP — the documented production shape (TLS terminator → Hub).
 *
 * The gate reads the Hub's own settings, never the request, so `secure_cookies`
 * is the fixture: it is what an operator behind a TLS terminator sets, and it
 * is the rung that says "this Hub is served over HTTPS" without naming a host.
 * Without this spec, a regression that read the LISTEN scheme instead would
 * silently stand ALTCHA down for every TLS-terminated deployment while the
 * unit tests still passed.
 */
const test = base.extend<{ server: DevServerHandle, tlsProxy: TlsProxyHandle }>({
  // eslint-disable-next-line no-empty-pattern
  server: async ({}, use) => {
    const server = await startDevServer({ dataDirPrefix: 'leapmux-e2e-captcha-https-proxy' })
    let cliConfigDir: string | undefined
    try {
      const cfg = await mintCLITokenForAdmin(server)
      cliConfigDir = cfg.path
      await setHubSetting(cfg, 'secure_cookies', 'true')
      await use(server)
    }
    finally {
      await stopDevServer(server, cliConfigDir ? [cliConfigDir] : [])
    }
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
