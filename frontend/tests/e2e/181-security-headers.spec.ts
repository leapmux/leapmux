import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { elevateSessionViaAPI, loginViaAPI, TEST_ADMIN_PASSWORD, TEST_ADMIN_USERNAME } from './helpers/api'
import { openTerminalViaUI } from './helpers/ui'

/**
 * The Hub derives the Content-Security-Policy from the assets it actually
 * serves (`frontend.Policy` in the Go backend), so the only honest check is a
 * real browser that loads a real build.
 *
 * A wrong policy is an OUTAGE, not a weaker defence: one inline script the
 * `script-src` hash does not cover leaves a blank page, and one directive too
 * strict silently stops the terminal renderer or the WebSocket. That is why
 * this spec watches for violations across a real flow rather than only reading
 * the header.
 */

/** Every CSP violation the browser reports, in arrival order. */
function collectCspViolations(page: Page): string[] {
  const violations: string[] = []
  // The console message is the one signal that covers BOTH a blocked resource
  // and a blocked inline script; `securitypolicyviolation` needs a listener in
  // the page, which a blocked script could itself prevent from installing.
  page.on('console', (msg) => {
    const text = msg.text()
    if (text.includes('Content Security Policy') || text.includes('Refused to'))
      violations.push(text)
  })
  return violations
}

test.describe('security headers', () => {
  test('serves a derived CSP and the transport headers on the app document', async ({ page }) => {
    const response = await page.goto('/')
    expect(response).not.toBeNull()

    const headers = response!.headers()

    // Enforced, not report-only. The shipped policy must take effect.
    const csp = headers['content-security-policy']
    expect(csp, 'the app document must carry an enforced CSP').toBeTruthy()
    expect(headers['content-security-policy-report-only']).toBeUndefined()

    // script-src carries a derived hash and never 'unsafe-inline', which would
    // discard the whole value of the directive.
    expect(csp).toContain('script-src')
    expect(csp).toContain('\'sha256-')
    expect(csp).not.toContain('script-src \'self\' \'unsafe-inline\'')

    expect(csp).toContain('frame-ancestors \'none\'')
    expect(csp).toContain('base-uri \'self\'')
    expect(csp).toContain('object-src \'none\'')

    expect(headers['x-content-type-options']).toBe('nosniff')
    expect(headers['referrer-policy']).toBe('same-origin')
  })

  // /elevate is a step-up prompt that runs a WebAuthn ceremony, so a policy
  // that reaches it without the derived script-src hash is not a weaker
  // defence -- it is a blank page where a user has to prove who they are,
  // and the CLI login that sent the browser here waits for ever. It sits
  // OUTSIDE the (app) group, so it is worth its own check rather than an
  // assumption that the shell's coverage extends to it.
  test('serves the same enforced CSP on the standalone /elevate route', async ({ page }) => {
    const violations = collectCspViolations(page)
    const response = await page.goto('/elevate?redirect=%2F')
    expect(response).not.toBeNull()

    const csp = response!.headers()['content-security-policy']
    expect(csp, '/elevate must carry the same enforced CSP as the app document').toBeTruthy()
    expect(csp).toContain('\'sha256-')
    expect(response!.headers()['x-content-type-options']).toBe('nosniff')

    // It is an SPA route, so the shell must actually boot here: a redirect
    // to /login (no session) is the expected end state for an anonymous
    // visitor, and either way the document ran its scripts to decide.
    await expect.poll(() => new URL(page.url()).pathname).toMatch(/^\/(elevate|login)$/)
    expect(violations, `the browser refused something the policy should allow:\n${violations.join('\n')}`)
      .toEqual([])
  })

  // The headers wrap the whole mux, not the frontend handler alone, so a
  // non-document response carries them too.
  test('serves the transport headers on a non-document route', async ({ page, leapmuxServer }) => {
    const response = await page.request.get(`${leapmuxServer.hubUrl}/version`)
    expect(response.ok()).toBe(true)
    expect(response.headers()['x-content-type-options']).toBe('nosniff')
  })

  // The real check. The app boots, opens a WebSocket, renders markdown and
  // mounts the xterm renderer -- each of which a too-strict directive breaks.
  test('boots the app, a terminal and a chat with no CSP violation', async ({ page, authenticatedWorkspace }) => {
    const violations = collectCspViolations(page)

    // RELOAD, so the listener is armed for the boot it claims to cover. The
    // workspace fixture signs in and renders the shell BEFORE the test body
    // runs, so the browser emitted every violation from that first boot -- a
    // font, an image, a stylesheet or a worker the policy refuses -- before
    // this listener existed. The visibility assertion below caught a blocked
    // SCRIPT and nothing else, and script-src is the one directive the derived
    // hashes already cover; the subresource directives had no coverage at all.
    await page.reload()

    // The boot runs again under the listener: the inline manifest executes, the
    // module chunk loads, the channel WebSocket connects, and the browser
    // fetches the fonts and images the shell needs.
    await expect(page.locator('[data-testid="tab-bar"]').first()).toBeVisible()

    // The terminal is the directive-sensitive surface: xterm's DOM renderer
    // writes a <style> element's textContent, which style-src governs.
    await openTerminalViaUI(page)
    await expect(page.locator('.xterm').first()).toBeVisible()

    expect(violations, `the browser refused something the policy should allow:\n${violations.join('\n')}`)
      .toEqual([])
  })

  /**
   * The guard on the test above. "No violations" proves nothing while the
   * detector cannot fire and the policy does not take effect, and both
   * failures are silent -- a hub that dropped the header entirely would pass
   * that test.
   *
   * So inject the payload the policy exists to stop. `page.evaluate` runs over
   * CDP and is not itself subject to CSP, but a <script> element it appends to
   * the document IS, and its body matches no `sha256-` source in the header.
   */
  test('refuses an injected inline script, and the detector sees it', async ({ page }) => {
    const violations = collectCspViolations(page)
    await page.goto('/')

    const executed = await page.evaluate(() => {
      const el = document.createElement('script')
      el.textContent = 'window.__cspProbe = true'
      document.head.appendChild(el)
      return (window as unknown as { __cspProbe?: boolean }).__cspProbe === true
    })

    expect(executed, 'an inline script the policy does not hash must not run').toBe(false)
    // `page.on('console')` fills this array over CDP, on a different message
    // stream from the `evaluate` response above, so a bare value `expect` reads
    // it at one instant. `expect.poll` retries under the GLOBAL expect timeout,
    // with no per-call override -- the E2E rule in CLAUDE.md forbids one.
    //
    // This spec deliberately does NOT poll the mirror assertion above:
    // `expect.poll` retries until an assertion PASSES, so polling "still empty"
    // would succeed on its first evaluation and catch nothing extra.
    await expect.poll(
      () => violations.length,
      { message: 'the violation detector must see the refusal it exists to catch' },
    ).toBeGreaterThan(0)
  })
})

/**
 * The authorization server's own pages, and the two directives that decide
 * where a consent can send a browser.
 *
 * `form-action` is the load-bearing one and it is per RESPONSE. The app
 * document's policy says `'self'`, because nothing the SPA renders should post
 * off-origin; a consent page must additionally allow the ONE address its app
 * registered, or the browser blocks the redirect and the app never gets a code.
 *
 * The old policy listed every loopback port in the app document instead, which
 * is both too wide there (any page could post to any local listener) and too
 * narrow here (an `https` app has no loopback address at all). Only a real
 * browser walking a real consent shows either.
 */
test.describe('the authorization server pages', () => {
  const CONTROL_CLI_CLIENT_ID = 'leapmux-control-cli'

  test('the app document allows no off-origin form post', async ({ page }) => {
    const response = await page.goto('/')
    const csp = response!.headers()['content-security-policy'] ?? ''

    expect(csp).toContain('form-action \'self\'')
    // No loopback origin in the app document's policy. A page that lists one
    // lets ANY script on it post to a local listener.
    expect(csp).not.toContain('127.0.0.1:')
    expect(csp).not.toContain('http://localhost')
  })

  test('a consent page allows only the address its app registered', async ({ page, leapmuxServer }) => {
    // An ELEVATED session, so the request reaches the consent page itself.
    // Anonymously the gate bounces to /login and the response that comes back
    // carries the SPA's policy, which is a different page and a different
    // answer.
    const cookie = await loginViaAPI(leapmuxServer.hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD)
    await elevateSessionViaAPI(leapmuxServer.hubUrl, cookie, TEST_ADMIN_PASSWORD)

    const params = new URLSearchParams({
      client_id: CONTROL_CLI_CLIENT_ID,
      response_type: 'code',
      code_challenge_method: 'S256',
      redirect_uri: 'http://127.0.0.1:54321/callback',
      state: 'state-csp',
      code_challenge: 'E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM',
      installation_name: 'e2e-laptop',
    })
    const res = await page.request.get(
      `${leapmuxServer.hubUrl}/oauth/authorize?${params.toString()}`,
      { headers: { Cookie: cookie }, maxRedirects: 0 },
    )
    expect(res.status()).toBe(200)
    const csp = res.headers()['content-security-policy'] ?? ''
    expect(csp, 'a consent page must carry its own policy').toBeTruthy()

    // A page that renders NOTHING scriptable. `default-src 'none'` covers
    // script-src, so a WebAuthn ceremony is impossible here -- which is exactly
    // why the gate bounces to the SPA's /elevate instead of prompting in place.
    expect(csp).toContain('default-src \'none\'')
    expect(csp).not.toContain('\'unsafe-eval\'')
    expect(csp).not.toContain('sha256-')
    expect(csp).toContain('frame-ancestors \'none\'')
    expect(csp).toContain('base-uri \'none\'')

    // form-action names 'self' plus the app's OWN address, and only those.
    //
    // The port is a WILDCARD because this app's registered address is a
    // loopback one: RFC 8252 section 7.3 lets a native app bind any free port,
    // so the hub matches every port on that host and the policy must agree. A
    // policy that named the one port the launch happened to pick would block
    // the next launch, which picks a different one. A non-loopback app gets its
    // exact origin instead -- see redirectFormActionSource and its unit test.
    expect(csp).toContain('form-action \'self\' http://127.0.0.1:*')
    expect(csp).not.toContain('http://localhost')
    expect(csp).not.toContain('https://')
  })
})
