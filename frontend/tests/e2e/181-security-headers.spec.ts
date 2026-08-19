import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { openTerminalViaUI } from './helpers/ui'

/**
 * The Content-Security-Policy is derived from the assets the Hub actually
 * serves (`frontend.Policy` in the Go backend), so the only honest check is a
 * real browser loading a real build.
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

    // Enforced, not report-only. The shipped policy must bite.
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

    // The workspace fixture already signed in and rendered the shell, which
    // means the inline manifest ran, the module chunk loaded and the channel
    // WebSocket connected. A blocked script would leave none of this on screen.
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
   * detector cannot fire and the policy does not bite, and both failures are
   * silent -- a hub that dropped the header entirely would pass that test.
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
    expect(violations.length, 'the violation detector must see the refusal it exists to catch')
      .toBeGreaterThan(0)
  })
})
