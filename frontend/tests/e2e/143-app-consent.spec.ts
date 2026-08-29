/**
 * The app consent gate and the /elevate bounce.
 *
 * A unit test cannot catch what this spec exists for. The hub sends the
 * browser to `/elevate?redirect=/oauth/authorize...`, and `/oauth/authorize`
 * is a Go mux route, not an entry in this application's route table -- so a
 * client-side `navigate()` back to it renders the SPA's 404 page while the
 * app that asked waits for a consent screen nobody ever sees. Only a real
 * browser that walks the whole round trip shows that.
 */

import { expect, test } from './fixtures'
import { elevateSessionViaAPI, loginViaAPI, signUpViaAPI, TEST_ADMIN_PASSWORD, TEST_ADMIN_USERNAME } from './helpers/api'
import { loginViaToken } from './helpers/ui'

/**
 * The built-in control CLI's registration. Every flow here authorizes it,
 * because it is the one app a fresh hub ships with -- and the one whose
 * redirect address is a constant of the build.
 */
const CONTROL_CLI_CLIENT_ID = 'leapmux-control-cli'

/** A syntactically valid OAuth 2.1 authorization URL for a loopback listener. */
function startURL(hubUrl: string, extra: Record<string, string> = {}): string {
  const params = new URLSearchParams({
    client_id: CONTROL_CLI_CLIENT_ID,
    response_type: 'code',
    code_challenge_method: 'S256',
    redirect_uri: 'http://127.0.0.1:54321/callback',
    state: `state-${Date.now()}`,
    code_challenge: 'E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM',
    installation_name: 'e2e-laptop',
    ...extra,
  })
  return `${hubUrl}/oauth/authorize?${params.toString()}`
}

/**
 * A FRESH admin session, never elevated.
 *
 * The fixture's shared `adminToken` outlives every test in the file, and an
 * elevation lasts two hours -- so a test that reused it would pass or fail
 * on whether some earlier test happened to elevate it. Each test that needs
 * an un-elevated session mints its own.
 */
async function freshAdminSession(hubUrl: string): Promise<string> {
  return loginViaAPI(hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD)
}

test.describe('app consent needs an elevated session', () => {
  test('bounces through /elevate and returns to the consent page', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, await freshAdminSession(leapmuxServer.hubUrl))

    await page.goto(startURL(leapmuxServer.hubUrl))

    // Layer one: the hub sends the un-elevated session to the SPA prompt,
    // with the consent URL as the return address.
    await expect(page).toHaveURL(/\/elevate\?redirect=/)
    await expect(page.getByTestId('elevate-card')).toBeVisible()

    await page.getByTestId('elevate-password').fill(TEST_ADMIN_PASSWORD)
    await page.getByTestId('elevate-password-submit').click()

    // Back on the HUB's page, not the SPA's 404. This is the assertion the
    // whole spec exists for.
    await expect(page).toHaveURL(/\/oauth\/authorize\?/)
    await expect(page.getByRole('button', { name: 'Allow' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Authorize an unverified app?' })).toBeVisible()
  })

  test('an already-elevated session reaches the consent page directly', async ({ page, leapmuxServer }) => {
    const cookie = await freshAdminSession(leapmuxServer.hubUrl)
    await elevateSessionViaAPI(leapmuxServer.hubUrl, cookie, TEST_ADMIN_PASSWORD)
    await loginViaToken(page, cookie)

    await page.goto(startURL(leapmuxServer.hubUrl))

    await expect(page).toHaveURL(/\/oauth\/authorize\?/)
    await expect(page.getByRole('button', { name: 'Allow' })).toBeVisible()
  })

  test('the elevated marker stops the second bounce instead of admitting', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, await freshAdminSession(leapmuxServer.hubUrl))

    // The marker is what the hub itself appends to the return address. A
    // request that carries it and is STILL not elevated must get an
    // explanation, not another redirect -- and must not be admitted.
    await page.goto(startURL(leapmuxServer.hubUrl, { elevated: '1' }))

    await expect(page).toHaveURL(/\/oauth\/authorize\?/)
    await expect(page.getByRole('heading', { name: 'Verify your identity' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Allow' })).toHaveCount(0)
  })

  test('refuses the consent POST rather than redirecting it', async ({ page, leapmuxServer }) => {
    // The POST carries the PKCE challenge in its BODY, so a redirect would
    // destroy the flow irrecoverably. The hub refuses it instead.
    const cookie = await freshAdminSession(leapmuxServer.hubUrl)
    const res = await page.request.post(`${leapmuxServer.hubUrl}/oauth/consent`, {
      headers: { 'Content-Type': 'application/x-www-form-urlencoded', 'Cookie': cookie },
      form: {
        client_id: CONTROL_CLI_CLIENT_ID,
        response_type: 'code',
        code_challenge_method: 'S256',
        redirect_uri: 'http://127.0.0.1:54321/callback',
        state: 'state-post',
        code_challenge: 'E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM',
        installation_name: 'e2e-laptop',
        decision: 'allow',
      },
      maxRedirects: 0,
    })
    expect(res.status()).toBe(403)
    expect(await res.text()).toContain('Verify your identity')
  })

  test('refuses an admin scope from an account that is not an administrator', async ({ page, leapmuxServer }) => {
    const username = `plain-cli-${Date.now()}`
    const password = 'password123'
    const cookie = await signUpViaAPI(
      leapmuxServer.hubUrl,
      username,
      password,
      'Plain CLI',
      `${username}@test.local`,
    )
    await elevateSessionViaAPI(leapmuxServer.hubUrl, cookie, password)

    const res = await page.request.get(startURL(leapmuxServer.hubUrl, { scope: 'admin:read' }), {
      headers: { Cookie: cookie },
      maxRedirects: 0,
    })

    // It REDIRECTS the refusal to the app, per RFC 6749 section 4.1.2.1: the
    // client and its address are both registered by this point, so the answer
    // belongs to the client. A hub page instead would leave a third-party app
    // waiting on a callback that never arrives.
    expect(res.status()).toBe(302)
    const location = res.headers().location ?? ''
    expect(location).toContain('127.0.0.1:54321')
    expect(location).toContain('error=access_denied')
    // `+` for a space, because the query is form-encoded and
    // decodeURIComponent does not undo that half.
    expect(decodeURIComponent(location).replaceAll('+', ' ')).toContain('not a hub administrator')
    expect(location).not.toContain('code=')
  })

  test('states the admin grant on the consent page for an administrator', async ({ page, leapmuxServer }) => {
    const cookie = await freshAdminSession(leapmuxServer.hubUrl)
    await elevateSessionViaAPI(leapmuxServer.hubUrl, cookie, TEST_ADMIN_PASSWORD)
    await loginViaToken(page, cookie)

    await page.goto(startURL(leapmuxServer.hubUrl, { scope: 'admin:read admin:users' }))

    // The browser is where the consent happens, so the page -- not only the
    // app that asked -- says what the credential will be able to do, in
    // sentences rather than in scope tokens.
    await expect(page.getByText(/Read this hub.s administration/)).toBeVisible()
    await expect(page.getByRole('button', { name: 'Allow' })).toBeVisible()
  })

  // An unverified app's chosen NAME never enters the heading.
  //
  // The registrant chooses that string, and open registration lets an
  // anonymous caller be the registrant. A name in the h1 would let it read as
  // the hub's own words, so the heading is hub-authored and the name appears
  // inside a paragraph that says the app claims it.
  test('keeps an unverified app name out of the heading', async ({ page, leapmuxServer }) => {
    const cookie = await freshAdminSession(leapmuxServer.hubUrl)
    await elevateSessionViaAPI(leapmuxServer.hubUrl, cookie, TEST_ADMIN_PASSWORD)
    await loginViaToken(page, cookie)

    await page.goto(startURL(leapmuxServer.hubUrl))

    const heading = page.getByRole('heading', { name: 'Authorize an unverified app?' })
    await expect(heading).toBeVisible()
    await expect(heading).not.toContainText('LeapMux control CLI')
    await expect(page.getByText(/Nobody has verified this app on this hub/)).toBeVisible()
  })

  // DENY returns to the app immediately, with the standard error.
  //
  // Without it the app waited out the whole code TTL to learn that a person
  // refused, and a user who clicks Deny got no sign that anything happened.
  test('deny returns access_denied to the app and mints no code', async ({ page, leapmuxServer }) => {
    const cookie = await freshAdminSession(leapmuxServer.hubUrl)
    await elevateSessionViaAPI(leapmuxServer.hubUrl, cookie, TEST_ADMIN_PASSWORD)

    const res = await page.request.post(`${leapmuxServer.hubUrl}/oauth/consent`, {
      headers: { 'Content-Type': 'application/x-www-form-urlencoded', 'Cookie': cookie },
      form: {
        client_id: CONTROL_CLI_CLIENT_ID,
        response_type: 'code',
        code_challenge_method: 'S256',
        redirect_uri: 'http://127.0.0.1:54321/callback',
        state: 'state-deny',
        code_challenge: 'E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM',
        installation_name: 'e2e-laptop',
        decision: 'deny',
      },
      maxRedirects: 0,
    })

    expect(res.status()).toBe(302)
    const location = res.headers().location ?? ''
    expect(location).toContain('error=access_denied')
    expect(location).toContain('state=state-deny')
    expect(location).not.toContain('code=')
  })
})
