import type { DevServerHandle } from './helpers/devServer'
import { expect } from '@playwright/test'
import { devServerTest } from './dev-server-fixtures'
import { callHub } from './helpers/api'
import { runCLI } from './helpers/cli'
import { withAdminConfiguredDevServer } from './helpers/devServer'
import { retryUntilPass } from './helpers/retryUntilPass'

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

/** The external captcha providers that this spec selects, as the CLI spells them. */
type ExternalCaptchaProvider = 'turnstile' | 'recaptcha_v3'

// Connect-JSON renders proto enums as their protojson name strings, so the
// wait below compares against the wire spelling.
function providerWireName(provider: ExternalCaptchaProvider): string {
  return `CAPTCHA_PROVIDER_${provider.toUpperCase()}`
}

// The hub caches the captcha config for ~30s and the dev-server seeding
// (via the default altcha provider) primes that cache, so the provider
// switch is only confirmed once system info reports the target provider —
// everything after that is guaranteed to exercise the external field
// rather than a stale altcha widget.
// A read can fail while the hub applies the change, so a failed read starts
// the next attempt. The wait ends before the test's own deadline, and its
// failure states the last read.
async function waitForSystemInfoProvider(hubUrl: string, provider: ExternalCaptchaProvider): Promise<void> {
  await retryUntilPass(async () => {
    const info = await callHub<{ captchaProvider?: string }>(hubUrl, 'AuthService/GetSystemInfo', {}, { operation: 'waitForSystemInfoProvider' })
    expect(info.captchaProvider, `the hub reports the captcha provider ${provider}`).toBe(providerWireName(provider))
  })
}

async function setupServerWithProvider(
  provider: ExternalCaptchaProvider,
  siteKey: string,
  secret: string,
  use: (server: DevServerHandle) => Promise<void>,
): Promise<void> {
  // Captcha configuration is an ONLINE admin RPC: `leapmux control admin
  // captcha set` against the running hub, authenticated as the admin.
  // There is no offline captcha verb any more, so the CLI needs a minted
  // bearer rather than the hub's data dir.
  await withAdminConfiguredDevServer(`leapmux-e2e-captcha-${provider}`, async (cfg, server) => {
    await runCLI(cfg, [
      'admin',
      'captcha',
      'set',
      '--provider',
      provider,
      '--site-key',
      siteKey,
      '--secret',
      secret,
    ])
    await waitForSystemInfoProvider(server.hubUrl, provider)

    // `captcha show` reports the switch and never the secret. It answers
    // with one entry per captcha settings key.
    const shown = await runCLI(cfg, ['admin', 'captcha', 'show']) as Record<string, { effective_json?: unknown }>
    expect(shown['captcha.selected']?.effective_json).toBe(provider)
    expect(JSON.stringify(shown)).not.toContain(secret)
  }, use)
}

// One dedicated hub per provider, mirroring the 179 spec's per-algorithm
// servers: the provider switch must not leak into the shared fixture's
// hub.
const turnstileTest = devServerTest<DevServerHandle>(use => setupServerWithProvider('turnstile', TURNSTILE_SITE_KEY, TURNSTILE_SECRET, use))

const recaptchaTest = devServerTest<DevServerHandle>(use => setupServerWithProvider('recaptcha_v3', 'recaptcha-site-key', 'recaptcha-secret', use))

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
    await expect(page.getByText(/captcha verification failed/i)).toBeVisible()
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
    await expect(submit).toBeEnabled()

    // The token was minted for the login procedure's action, which the
    // hub verifies server-side against the same string.
    const actions = await page.evaluate(() => (window as unknown as { __recaptchaActions?: string[] }).__recaptchaActions)
    expect(actions).toContain('login')

    // A fake token draws the uniform denial like any other provider.
    await submit.click()
    await expect(page.getByText(/captcha verification failed/i)).toBeVisible()
  })
})
