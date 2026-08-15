import type { DevServerHandle } from './helpers/devServer'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { test as base, expect } from '@playwright/test'
import { startDevServer, stopDevServer } from './helpers/devServer'
import { getGlobalState } from './helpers/server'

const execFileAsync = promisify(execFile)

// Cloudflare's documented dummy keys: the site key always passes client
// side and the secret always passes verification, so the spec never
// depends on a real Cloudflare account. The fake script below replaces
// the real one, so no challenge traffic leaves the machine either way —
// the hub's siteverify call sees a fake token and denies with the same
// uniform error whether the network is up or not.
const TURNSTILE_SITE_KEY = '1x00000000000000000000AA'
const TURNSTILE_SECRET = '1x0000000000000000000000000000000AA'

// A stand-in for Cloudflare's api.js: renders a checkbox that mints a
// fake token when checked, mirroring the render/callback/reset surface
// TurnstileField drives.
const FAKE_TURNSTILE_SCRIPT = `
  window.turnstile = {
    render(container, options) {
      const el = typeof container === 'string' ? document.querySelector(container) : container;
      const box = document.createElement('input');
      box.type = 'checkbox';
      box.dataset.turnstileCheckbox = '';
      box.addEventListener('change', () => {
        if (box.checked) options.callback && options.callback('fake-turnstile-token');
        else options['expired-callback'] && options['expired-callback']();
      });
      el.appendChild(box);
      return 'fake-widget';
    },
    reset() {
      const box = document.querySelector('[data-turnstile-checkbox]');
      if (box) box.checked = false;
    },
    remove() {},
    getResponse() { return undefined; },
    ready(cb) { cb(); },
  };
`

// A stand-in for Google's api.js: v3 has no visible widget, so the fake
// records every executed action for assertions and mints a token at once.
const FAKE_RECAPTCHA_SCRIPT = `
  window.__recaptchaActions = [];
  window.grecaptcha = {
    ready(cb) { cb(); },
    async execute(siteKey, options) {
      window.__recaptchaActions.push(options.action);
      return 'fake-recaptcha-token';
    },
  };
`

// The hub caches the captcha config for ~30s and the dev-server seeding
// (via the default altcha provider) primes that cache, so the provider
// switch is only confirmed once system info reports the target provider —
// everything after that is guaranteed to exercise the external field
// rather than a stale altcha widget.
// Connect-JSON renders proto enums as their protojson name strings, so
// the raw-fetch poll compares against the wire spelling.
function providerWireName(provider: 'turnstile' | 'recaptcha_v3'): string {
  return `CAPTCHA_PROVIDER_${provider.replace('recaptcha_v3', 'RECAPTCHA_V3').toUpperCase()}`
}

async function waitForSystemInfoProvider(hubUrl: string, provider: string, timeoutMs = 90_000): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`${hubUrl}/leapmux.v1.AuthService/GetSystemInfo`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({}),
      })
      if (res.ok) {
        const data = await res.json() as { captchaProvider?: string }
        if (data.captchaProvider === providerWireName(provider as 'turnstile' | 'recaptcha_v3'))
          return
      }
    }
    catch {
      // The dev server restarts during setup; retry until the deadline.
    }
    await new Promise(resolve => setTimeout(resolve, 500))
  }
  throw new Error(`hub did not report captcha provider ${provider} within ${timeoutMs}ms`)
}

async function setupServerWithProvider(
  provider: 'turnstile' | 'recaptcha_v3',
  siteKey: string,
  secret: string,
  use: (server: DevServerHandle) => Promise<void>,
): Promise<void> {
  const server = await startDevServer({ dataDirPrefix: `leapmux-e2e-captcha-${provider}` })
  try {
    const { binaryPath } = getGlobalState()
    // The dev/solo launcher nests the hub's data under {data-dir}/hub
    // (solo/solo.go), while `admin` addresses the production flat layout —
    // so the CLI must be pointed at the nested hub dir to reach the same
    // database the running dev hub uses.
    await execFileAsync(binaryPath, [
      'admin',
      'captcha',
      'set',
      '--provider',
      provider,
      '--site-key',
      siteKey,
      '--secret',
      secret,
      '--data-dir',
      `${server.dataDir}/hub`,
    ])
    await waitForSystemInfoProvider(server.hubUrl, provider)

    // `captcha show` reports the switch and never the secret.
    const { stdout } = await execFileAsync(binaryPath, ['admin', 'captcha', 'show', '--data-dir', `${server.dataDir}/hub`])
    const shown = JSON.parse(stdout) as Record<string, unknown>
    expect(shown.provider).toBe(provider)
    expect(JSON.stringify(shown)).not.toContain(secret)

    await use(server)
  }
  finally {
    await stopDevServer(server)
  }
}

// One dedicated hub per provider, mirroring the 179 spec's per-algorithm
// servers: the provider switch must not leak into the shared fixture's
// hub.
const turnstileTest = base.extend<{ server: DevServerHandle }>({
  // eslint-disable-next-line no-empty-pattern
  server: async ({}, use) => setupServerWithProvider('turnstile', TURNSTILE_SITE_KEY, TURNSTILE_SECRET, use),
  baseURL: async ({ server }, use) => {
    await use(server.hubUrl)
  },
})

const recaptchaTest = base.extend<{ server: DevServerHandle }>({
  // eslint-disable-next-line no-empty-pattern
  server: async ({}, use) => setupServerWithProvider('recaptcha_v3', 'recaptcha-site-key', 'recaptcha-secret', use),
  baseURL: async ({ server }, use) => {
    await use(server.hubUrl)
  },
})

turnstileTest.describe('captcha provider: turnstile', () => {
  turnstileTest('fake checkbox solves, submit unlocks, denial stays uniform', async ({ page }) => {
    await page.route('**challenges.cloudflare.com/turnstile/v0/api.js*', route =>
      route.fulfill({ contentType: 'application/javascript', body: FAKE_TURNSTILE_SCRIPT }))

    await page.goto('/login')
    await page.getByLabel('Username').fill('e2e-turnstile-user')
    await page.getByLabel('Password').fill('not-a-real-password')

    // Submit stays locked until the token exists; checking the fake
    // checkbox mints one through the callback, unlocking the form.
    const submit = page.getByRole('button', { name: 'Sign in' })
    const checkbox = page.locator('[data-turnstile-checkbox]')
    await expect(checkbox).toBeVisible()
    await expect(submit).toBeDisabled()
    await checkbox.check()
    await expect(submit).toBeEnabled()

    // The fake token fails the hub's real siteverify (or fails closed
    // without network); either way the denial is the uniform message.
    await submit.click()
    await expect(page.getByText(/captcha verification failed/i)).toBeVisible({ timeout: 30_000 })
  })
})

recaptchaTest.describe('captcha provider: recaptcha_v3', () => {
  recaptchaTest('token executes under the login action with no visible widget', async ({ page }) => {
    await page.route('**www.google.com/recaptcha/api.js*', route =>
      route.fulfill({ contentType: 'application/javascript', body: FAKE_RECAPTCHA_SCRIPT }))

    await page.goto('/login')
    await page.getByLabel('Username').fill('e2e-recaptcha-user')
    await page.getByLabel('Password').fill('not-a-real-password')

    // v3 is invisible: no checkbox, no altcha widget — and submit unlocks
    // once the executed token arrives.
    const submit = page.getByRole('button', { name: 'Sign in' })
    await expect(page.locator('altcha-widget')).toHaveCount(0)
    await expect(page.locator('[data-turnstile-checkbox]')).toHaveCount(0)
    await expect(submit).toBeEnabled({ timeout: 15_000 })

    // The token was minted for the login procedure's action, which the
    // hub verifies server-side against the same string.
    const actions = await page.evaluate(() => (window as unknown as { __recaptchaActions?: string[] }).__recaptchaActions)
    expect(actions).toContain('login')

    // A fake token draws the uniform denial like any other provider.
    await submit.click()
    await expect(page.getByText(/captcha verification failed/i)).toBeVisible({ timeout: 30_000 })
  })
})
