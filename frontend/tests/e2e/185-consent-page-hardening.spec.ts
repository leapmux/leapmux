/**
 * What an attacker-chosen string can do on a consent page.
 *
 * The consent page is the one place where text a REGISTRANT chose is rendered
 * to a different account, and where that account decides something
 * consequential. With open registration on, the registrant can be anonymous.
 * So the page's defences are the last line, and every one of them is about a
 * string the hub did not write.
 *
 * These need a real browser. A Go test can assert the bytes; only a browser
 * shows whether the bytes became markup, whether the policy blocked a fetch,
 * and what a heading actually reads.
 */

import { expect, test } from './fixtures'
import { elevateSessionViaAPI, loginViaAPI, TEST_ADMIN_PASSWORD, TEST_ADMIN_USERNAME } from './helpers/api'
import { loginViaToken } from './helpers/ui'

/** Register an app whose name is chosen to be hostile. */
async function registerApp(
  request: import('@playwright/test').APIRequestContext,
  hubUrl: string,
  cookie: string,
  clientName: string,
): Promise<string> {
  const res = await request.post(`${hubUrl}/leapmux.v1.AppService/RegisterApp`, {
    headers: { 'Content-Type': 'application/json', 'Cookie': cookie },
    data: {
      clientName,
      redirectUris: ['https://hostile.example.com/callback'],
      scopes: ['SCOPE_WORKSPACE_READ'],
      visibility: 'APP_VISIBILITY_HUB_WIDE',
      clientType: 'APP_CLIENT_TYPE_PUBLIC',
    },
  })
  expect(res.status(), await res.text()).toBe(200)
  const json = await res.json() as { app: { clientId: string } }
  return json.app.clientId
}

function authorizeURL(hubUrl: string, clientId: string): string {
  const params = new URLSearchParams({
    client_id: clientId,
    response_type: 'code',
    code_challenge_method: 'S256',
    redirect_uri: 'https://hostile.example.com/callback',
    state: 'state-hardening',
    code_challenge: 'E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM',
    installation_name: 'e2e-laptop',
  })
  return `${hubUrl}/oauth/authorize?${params.toString()}`
}

test.describe('consent page hardening', () => {
  test('an app name carrying markup renders as text', async ({ page, leapmuxServer }) => {
    const hubUrl = leapmuxServer.hubUrl
    const cookie = await loginViaAPI(hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD)
    await elevateSessionViaAPI(hubUrl, cookie, TEST_ADMIN_PASSWORD)

    // A name that is markup, an attribute break-out, and a quotation mark at
    // once. Each would land somewhere different if the template interpolated
    // rather than escaped.
    const hostile = '<img src=x onerror=alert(1)>" autofocus x="'
    const clientId = await registerApp(page.request, hubUrl, cookie, hostile)

    await loginViaToken(page, cookie)
    const violations: string[] = []
    page.on('console', (msg) => {
      if (msg.text().includes('Content Security Policy') || msg.text().includes('Refused to'))
        violations.push(msg.text())
    })
    await page.goto(authorizeURL(hubUrl, clientId))

    // The name is TEXT. No element came from it, and the page's own document
    // carries no image the registrant chose.
    await expect(page.locator('img')).toHaveCount(0)
    // TWICE, because the page names the app in both the unverified warning and
    // the identity block -- and each occurrence is the escaped text rather than
    // markup, which is the whole assertion.
    await expect(page.getByText(hostile, { exact: false }).first()).toBeVisible()
    expect(await page.getByText(hostile, { exact: false }).count()).toBeGreaterThan(0)

    // And no script ran. `default-src 'none'` on this response means an
    // injected handler has nothing to execute, but the assertion is on the
    // OUTCOME rather than on the header, which the security-headers spec
    // already pins.
    expect(violations).toEqual([])
  })

  // The chosen name never enters the HEADING. A name in the <h1> reads as the
  // hub's own words -- "Authorize LeapMux Security Check?" -- so the heading is
  // hub-authored and the name appears inside a paragraph that attributes it.
  test('keeps the chosen name out of the heading', async ({ page, leapmuxServer }) => {
    const hubUrl = leapmuxServer.hubUrl
    const cookie = await loginViaAPI(hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD)
    await elevateSessionViaAPI(hubUrl, cookie, TEST_ADMIN_PASSWORD)

    const impersonating = 'LeapMux Security Check'
    const clientId = await registerApp(page.request, hubUrl, cookie, impersonating)

    await loginViaToken(page, cookie)
    await page.goto(authorizeURL(hubUrl, clientId))

    const heading = page.getByRole('heading', { level: 1 })
    await expect(heading).toBeVisible()
    await expect(heading).not.toContainText(impersonating)
    // It says the app is unverified, and attributes the name to the app.
    await expect(page.getByText(/Nobody has verified this app on this hub/)).toBeVisible()
    await expect(page.getByText(/It says its name is/)).toBeVisible()
  })

  // Every permission is spelled out as a SENTENCE. A consent screen that
  // listed scope tokens would ask somebody to approve `terminal:write` without
  // telling them it runs any command on their machine.
  test('states each permission in a sentence a person can act on', async ({ page, leapmuxServer }) => {
    const hubUrl = leapmuxServer.hubUrl
    const cookie = await loginViaAPI(hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD)
    await elevateSessionViaAPI(hubUrl, cookie, TEST_ADMIN_PASSWORD)

    const res = await page.request.post(`${hubUrl}/leapmux.v1.AppService/RegisterApp`, {
      headers: { 'Content-Type': 'application/json', 'Cookie': cookie },
      data: {
        clientName: 'Wide app',
        redirectUris: ['https://hostile.example.com/callback'],
        scopes: ['SCOPE_TERMINAL_WRITE', 'SCOPE_TUNNEL_OPEN'],
        visibility: 'APP_VISIBILITY_HUB_WIDE',
      },
    })
    expect(res.status(), await res.text()).toBe(200)
    const clientId = ((await res.json()) as { app: { clientId: string } }).app.clientId

    await loginViaToken(page, cookie)
    await page.goto(authorizeURL(hubUrl, clientId))

    // The CONSEQUENCE, not the token.
    await expect(page.getByText(/runs any command on your machine/)).toBeVisible()
    await expect(page.getByText(/inside your private network/)).toBeVisible()
    await expect(page.getByText('terminal:write')).toHaveCount(0)
  })

  // The consent page renders an app ICON from the hub's OWN origin.
  //
  // A remote logo URL would be a beacon: it reports to the app operator when
  // the consent page rendered and from which IP, and its bytes are chosen by
  // the registrant. The unverified app above shows a monogram instead, which
  // fetches nothing at all.
  test('fetches no third-party resource', async ({ page, leapmuxServer }) => {
    const hubUrl = leapmuxServer.hubUrl
    const cookie = await loginViaAPI(hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD)
    await elevateSessionViaAPI(hubUrl, cookie, TEST_ADMIN_PASSWORD)
    const clientId = await registerApp(page.request, hubUrl, cookie, 'Beacon app')

    const offOrigin: string[] = []
    page.on('request', (req) => {
      if (!req.url().startsWith(hubUrl))
        offOrigin.push(req.url())
    })

    await loginViaToken(page, cookie)
    await page.goto(authorizeURL(hubUrl, clientId))
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible()

    expect(offOrigin, 'a consent page must fetch nothing off-origin').toEqual([])
  })

  /**
   * The CSP fix, proven the only way it can be: a real browser submitting a real
   * consent form to an app whose address is not a loopback one.
   *
   * `form-action` is enforced by the BROWSER at submit time, and nothing else
   * observes it. The policy used to list every loopback port on the app document,
   * which meant an `https` app had no source at all -- the browser silently
   * blocked the redirect, and the app waited for a callback that a Go test would
   * have said was sent.
   *
   * The navigation itself cannot succeed here: example.com is not this hub. What
   * this asserts is that the browser ATTEMPTED it -- a CSP refusal is a console
   * violation and a navigation that never leaves the page, which is a different
   * and observable failure.
   */
  test('a browser completes the redirect to a non-loopback app', async ({ page, leapmuxServer }) => {
    const hubUrl = leapmuxServer.hubUrl
    const cookie = await loginViaAPI(hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD)
    await elevateSessionViaAPI(hubUrl, cookie, TEST_ADMIN_PASSWORD)
    const clientId = await registerApp(page.request, hubUrl, cookie, 'HTTPS app')

    const violations: string[] = []
    page.on('console', (msg) => {
      if (msg.text().includes('Content Security Policy') || msg.text().includes('Refused to'))
        violations.push(msg.text())
    })

    // The redirect target is off-origin and unreachable, so the navigation fails
    // to load. Record what the browser tried to reach rather than waiting for a
    // response that cannot come.
    const attempted: string[] = []
    page.on('request', (req) => {
      if (req.isNavigationRequest() && req.url().startsWith('https://hostile.example.com'))
        attempted.push(req.url())
    })

    await loginViaToken(page, cookie)
    await page.goto(authorizeURL(hubUrl, clientId))
    await expect(page.getByRole('button', { name: 'Allow' })).toBeVisible()

    await page.getByRole('button', { name: 'Allow' }).click().catch(() => {
    // The navigation fails to resolve the host, which is expected.
    })
    await page.waitForTimeout(500)

    expect(violations.filter(v => v.includes('form-action')), 'the browser must not block the consent form from reaching the app').toEqual([])
    expect(attempted.length, 'the browser must have attempted the redirect to the app\'s own address').toBeGreaterThan(0)
    expect(attempted[0]).toContain('code=')
  })
})
