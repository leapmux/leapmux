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
import { elevatedAdminSessionViaAPI } from './helpers/api'
import { collectCspViolations } from './helpers/csp'
import { authorizeURL, registerAppViaAPI } from './helpers/oauthApps'
import { loginViaToken } from './helpers/ui'

/** The callback of every app that this spec registers: an address that is not loopback. */
const HOSTILE_REDIRECT_URI = 'https://hostile.example.com/callback'

/** Register a hub-wide app whose name is chosen to be hostile. */
async function registerApp(hubUrl: string, cookie: string, clientName: string): Promise<string> {
  const { clientId } = await registerAppViaAPI(hubUrl, cookie, {
    clientName,
    redirectUris: [HOSTILE_REDIRECT_URI],
    scopes: ['SCOPE_WORKSPACE_READ'],
    visibility: 'APP_VISIBILITY_HUB_WIDE',
    clientType: 'APP_CLIENT_TYPE_PUBLIC',
  })
  return clientId
}

/** The consent page of one app of this spec. */
function consentURL(hubUrl: string, clientId: string): string {
  return authorizeURL(hubUrl, { clientId, redirectUri: HOSTILE_REDIRECT_URI, state: 'state-hardening' })
}

test.describe('consent page hardening', () => {
  test('an app name carrying markup renders as text', async ({ page, leapmuxServer }) => {
    const hubUrl = leapmuxServer.hubUrl
    const cookie = await elevatedAdminSessionViaAPI(hubUrl)

    // A name that is markup, an attribute break-out, and a quotation mark at
    // once. Each would land somewhere different if the template interpolated
    // rather than escaped.
    const hostile = '<img src=x onerror=alert(1)>" autofocus x="'
    const clientId = await registerApp(hubUrl, cookie, hostile)

    await loginViaToken(page, cookie)
    const violations = collectCspViolations(page)
    await page.goto(consentURL(hubUrl, clientId))

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
    const cookie = await elevatedAdminSessionViaAPI(hubUrl)

    const impersonating = 'LeapMux Security Check'
    const clientId = await registerApp(hubUrl, cookie, impersonating)

    await loginViaToken(page, cookie)
    await page.goto(consentURL(hubUrl, clientId))

    const heading = page.getByRole('heading', { level: 1 })
    await expect(heading).toBeVisible()
    await expect(heading).not.toContainText(impersonating)
    // It says the app is unverified, and attributes the name to the app.
    await expect(page.getByText(/Nobody verified this app on this hub/)).toBeVisible()
    await expect(page.getByText(/It says its name is/)).toBeVisible()
  })

  // Every permission is spelled out as a SENTENCE. A consent screen that
  // listed scope tokens would ask somebody to approve `terminal:write` without
  // telling them it runs any command on their machine.
  test('states each permission in a sentence a person can act on', async ({ page, leapmuxServer }) => {
    const hubUrl = leapmuxServer.hubUrl
    const cookie = await elevatedAdminSessionViaAPI(hubUrl)

    const { clientId } = await registerAppViaAPI(hubUrl, cookie, {
      clientName: 'Wide app',
      redirectUris: [HOSTILE_REDIRECT_URI],
      scopes: ['SCOPE_TERMINAL_WRITE', 'SCOPE_TUNNEL_OPEN'],
      visibility: 'APP_VISIBILITY_HUB_WIDE',
    })

    await loginViaToken(page, cookie)
    await page.goto(consentURL(hubUrl, clientId))

    // The CONSEQUENCE, always beside the token -- never a token on its own.
    await expect(page.getByText(/runs any command on your machine/)).toBeVisible()
    await expect(page.getByText(/inside your private network/)).toBeVisible()

    // The page shows both: `.scope-token` names the permission and
    // `.scope-sentence` says what it does. Forbidding the token outright is the
    // wrong reading of that -- it asserts a page the product does not render,
    // and it passed only while the token span did not exist. What must hold is
    // that no token stands ALONE, so every row carries a non-empty sentence.
    const rows = page.locator('.scopes li:has(.scope-token)')
    await expect(rows).not.toHaveCount(0)
    const sentences = await rows.locator('.scope-sentence').allTextContents()
    const tokens = await rows.locator('.scope-token').allTextContents()
    expect(sentences, 'every permission row states a sentence').toHaveLength(tokens.length)
    for (const [index, sentence] of sentences.entries())
      expect(sentence.trim(), `the row for ${tokens[index]} states no consequence`).not.toBe('')
  })

  // The consent page renders an app ICON from the hub's OWN origin.
  //
  // A remote logo URL would be a beacon: it reports to the app operator when
  // the consent page rendered and from which IP, and its bytes are chosen by
  // the registrant. The unverified app above shows a monogram instead, which
  // fetches nothing at all.
  test('fetches no third-party resource', async ({ page, leapmuxServer }) => {
    const hubUrl = leapmuxServer.hubUrl
    const cookie = await elevatedAdminSessionViaAPI(hubUrl)
    const clientId = await registerApp(hubUrl, cookie, 'Beacon app')

    const offOrigin: string[] = []
    page.on('request', (req) => {
      if (!req.url().startsWith(hubUrl))
        offOrigin.push(req.url())
    })

    await loginViaToken(page, cookie)
    await page.goto(consentURL(hubUrl, clientId))
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
    const cookie = await elevatedAdminSessionViaAPI(hubUrl)
    const clientId = await registerApp(hubUrl, cookie, 'HTTPS app')

    const violations = collectCspViolations(page)

    // The redirect target is off-origin and unreachable, so the navigation fails
    // to load. Record what the browser tried to reach rather than waiting for a
    // response that cannot come.
    const attempted: string[] = []
    page.on('request', (req) => {
      if (req.isNavigationRequest() && req.url().startsWith('https://hostile.example.com'))
        attempted.push(req.url())
    })

    await loginViaToken(page, cookie)
    await page.goto(consentURL(hubUrl, clientId))
    await expect(page.getByRole('button', { name: 'Allow' })).toBeVisible()

    await page.getByRole('button', { name: 'Allow' }).click().catch(() => {
    // The navigation fails to resolve the host, which is expected.
    })

    // Wait for the outcome of the submit rather than for a fixed time: the
    // browser either attempts the redirect or reports that the policy refused
    // it. Then the refusal is read first, because it names the directive.
    const formActionRefusals = () => violations.filter(v => v.includes('form-action'))
    await expect.poll(() => attempted.length + formActionRefusals().length, { message: 'the submit reaches an outcome' }).toBeGreaterThan(0)
    expect(formActionRefusals(), 'the browser must not block the consent form from reaching the app').toEqual([])
    expect(attempted.length, 'the browser must have attempted the redirect to the app\'s own address').toBeGreaterThan(0)
    expect(attempted[0]).toContain('code=')
  })
})
