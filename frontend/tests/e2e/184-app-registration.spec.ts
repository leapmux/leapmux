/**
 * App REGISTRATION, and the visibility rule that decides who may authorize
 * what.
 *
 * The rule is one column on the hub -- `oauth_clients.owner_user_id`, NULL for
 * hub-wide -- and a unit test can check the query. What it cannot check is the
 * consequence a second account actually meets: whether the consent form for
 * somebody else's private app renders at all. That is the assertion here, and
 * it needs two real accounts and a real browser.
 */

import { expect, test } from './fixtures'
import {
  elevateSessionViaAPI,
  loginViaAPI,
  signUpViaAPI,
  TEST_ADMIN_PASSWORD,
  TEST_ADMIN_USERNAME,
  updateSettingViaAPI,
} from './helpers/api'
import { loginViaToken } from './helpers/ui'

/** Register an app through the Connect RPC, as the given session. */
async function registerApp(
  request: import('@playwright/test').APIRequestContext,
  hubUrl: string,
  cookie: string,
  body: Record<string, unknown>,
): Promise<{ clientId: string, clientSecret: string }> {
  const res = await request.post(`${hubUrl}/leapmux.v1.AppService/RegisterApp`, {
    headers: { 'Content-Type': 'application/json', 'Cookie': cookie },
    data: body,
  })
  expect(res.status(), await res.text()).toBe(200)
  const json = await res.json() as { app: { clientId: string }, clientSecret?: string }
  return { clientId: json.app.clientId, clientSecret: json.clientSecret ?? '' }
}

/** A consent URL for one app, with a syntactically valid PKCE challenge. */
function authorizeURL(hubUrl: string, clientId: string, redirectUri: string): string {
  const params = new URLSearchParams({
    client_id: clientId,
    response_type: 'code',
    code_challenge_method: 'S256',
    redirect_uri: redirectUri,
    state: 'state-visibility',
    code_challenge: 'E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM',
    installation_name: 'e2e-laptop',
  })
  return `${hubUrl}/oauth/authorize?${params.toString()}`
}

test.describe('app registration visibility', () => {
  test('a second account can neither see nor authorize a private app', async ({ page, leapmuxServer }) => {
    const hubUrl = leapmuxServer.hubUrl

    // OWNER: an ordinary account registers an app for itself.
    const ownerName = `app-owner-${Date.now()}`
    const ownerCookie = await signUpViaAPI(hubUrl, ownerName, 'password123', 'Owner', `${ownerName}@test.local`)
    // Registering is an elevated write on the hub (see appWriteGate), so every
    // account that registers here proves a factor first.
    await elevateSessionViaAPI(hubUrl, ownerCookie, 'password123')
    const { clientId } = await registerApp(page.request, hubUrl, ownerCookie, {
      clientName: 'Owner private app',
      redirectUris: ['https://owner.example.com/callback'],
      scopes: ['SCOPE_WORKSPACE_READ'],
      visibility: 'APP_VISIBILITY_PRIVATE',
      clientType: 'APP_CLIENT_TYPE_PUBLIC',
    })

    // The owner sees it in their own listing.
    const ownerList = await page.request.post(`${hubUrl}/leapmux.v1.AppService/ListApps`, {
      headers: { 'Content-Type': 'application/json', 'Cookie': ownerCookie },
      data: {},
    })
    expect(await ownerList.text()).toContain(clientId)

    // A SECOND account does not.
    const strangerName = `app-stranger-${Date.now()}`
    const strangerCookie = await signUpViaAPI(hubUrl, strangerName, 'password123', 'Stranger', `${strangerName}@test.local`)
    const strangerList = await page.request.post(`${hubUrl}/leapmux.v1.AppService/ListApps`, {
      headers: { 'Content-Type': 'application/json', 'Cookie': strangerCookie },
      data: {},
    })
    expect(await strangerList.text()).not.toContain(clientId)

    // And CANNOT authorize it. This is the assertion the spec exists for: a
    // listing that hides a row is worth nothing if the consent form still
    // renders for a stranger who was handed the link.
    await elevateSessionViaAPI(hubUrl, strangerCookie, 'password123')
    const consent = await page.request.get(
      authorizeURL(hubUrl, clientId, 'https://owner.example.com/callback'),
      { headers: { Cookie: strangerCookie }, maxRedirects: 0 },
    )
    const body = await consent.text()
    expect(body).not.toContain('Owner private app')
    expect(body).not.toContain('name="decision"')
    // invalid_client, on the hub's OWN page: the redirect address cannot be
    // trusted for a client this caller may not resolve, so nothing redirects.
    expect(consent.status()).toBe(400)
    expect(consent.headers().location).toBeUndefined()
  })

  test('an administrator app appears for a second account', async ({ page, leapmuxServer }) => {
    const hubUrl = leapmuxServer.hubUrl

    const adminCookie = await loginViaAPI(hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD)
    await elevateSessionViaAPI(hubUrl, adminCookie, TEST_ADMIN_PASSWORD)
    const { clientId } = await registerApp(page.request, hubUrl, adminCookie, {
      clientName: 'Hub-wide app',
      redirectUris: ['https://hubwide.example.com/callback'],
      scopes: ['SCOPE_WORKSPACE_READ'],
      visibility: 'APP_VISIBILITY_HUB_WIDE',
      clientType: 'APP_CLIENT_TYPE_PUBLIC',
    })

    const userName = `app-user-${Date.now()}`
    const userCookie = await signUpViaAPI(hubUrl, userName, 'password123', 'User', `${userName}@test.local`)
    await elevateSessionViaAPI(hubUrl, userCookie, 'password123')

    // It AUTHORIZES: the consent form renders for an account that does not own
    // it, which is what hub-wide means.
    const consent = await page.request.get(
      authorizeURL(hubUrl, clientId, 'https://hubwide.example.com/callback'),
      { headers: { Cookie: userCookie }, maxRedirects: 0 },
    )
    expect(consent.status()).toBe(200)
    const body = await consent.text()
    expect(body).toContain('Hub-wide app')
    expect(body).toContain('name="decision"')

    // It is READ-ONLY to them: the listing they may edit does not hold it, and
    // an edit is refused as not-found.
    const list = await page.request.post(`${hubUrl}/leapmux.v1.AppService/ListApps`, {
      headers: { 'Content-Type': 'application/json', 'Cookie': userCookie },
      data: {},
    })
    expect(await list.text()).not.toContain(clientId)

    const edit = await page.request.post(`${hubUrl}/leapmux.v1.AppService/UpdateApp`, {
      headers: { 'Content-Type': 'application/json', 'Cookie': userCookie },
      data: { clientId, clientName: 'stolen' },
    })
    expect(edit.status()).toBe(404)
  })

  // A non-administrator cannot register an app whose CEILING reaches hub
  // administration, and the refusal lands at REGISTRATION rather than at the
  // consent screen: a refusal there would arrive after the app existed and its
  // operator was told it was registered.
  test('refuses an admin ceiling from an ordinary account', async ({ page, leapmuxServer }) => {
    const hubUrl = leapmuxServer.hubUrl
    const name = `app-escalate-${Date.now()}`
    const cookie = await signUpViaAPI(hubUrl, name, 'password123', 'Escalator', `${name}@test.local`)
    // Elevated, so the refusal below is the CEILING rung rather than the
    // elevation gate one step in front of it.
    await elevateSessionViaAPI(hubUrl, cookie, 'password123')

    const res = await page.request.post(`${hubUrl}/leapmux.v1.AppService/RegisterApp`, {
      headers: { 'Content-Type': 'application/json', 'Cookie': cookie },
      data: {
        clientName: 'Escalating app',
        redirectUris: ['https://escalate.example.com/callback'],
        scopes: ['SCOPE_WORKSPACE_READ', 'SCOPE_ADMIN_USERS'],
        visibility: 'APP_VISIBILITY_PRIVATE',
      },
    })
    expect(res.status()).toBe(403)
    expect(await res.text()).toContain('admin:users')
  })

  // A hub-wide registration needs an administrator. The visibility field is
  // what a caller asks for; the hub decides.
  test('refuses a hub-wide registration from an ordinary account', async ({ page, leapmuxServer }) => {
    const hubUrl = leapmuxServer.hubUrl
    const name = `app-hubwide-${Date.now()}`
    const cookie = await signUpViaAPI(hubUrl, name, 'password123', 'Hopeful', `${name}@test.local`)
    // Elevated, so the refusal below is the VISIBILITY rung rather than the
    // elevation gate one step in front of it.
    await elevateSessionViaAPI(hubUrl, cookie, 'password123')

    const res = await page.request.post(`${hubUrl}/leapmux.v1.AppService/RegisterApp`, {
      headers: { 'Content-Type': 'application/json', 'Cookie': cookie },
      data: {
        clientName: 'Would-be catalogue app',
        redirectUris: ['https://hopeful.example.com/callback'],
        scopes: ['SCOPE_WORKSPACE_READ'],
        visibility: 'APP_VISIBILITY_HUB_WIDE',
      },
    })
    expect(res.status()).toBe(403)
    expect(await res.text()).toContain('administrator')
  })
})

test.describe('disconnecting an app', () => {
  /**
   * Mint one credential for an app, as the account's own administrator.
   *
   * Through `IssueAPIToken`, which is the headless mint an operator uses, so
   * the rows this test disconnects are the same shape a consent leg produces.
   */
  async function issueCredential(
    request: import('@playwright/test').APIRequestContext,
    hubUrl: string,
    cookie: string,
    userId: string,
    clientId: string,
    installationName: string,
  ): Promise<void> {
    const res = await request.post(`${hubUrl}/leapmux.v1.AdminUserService/IssueAPIToken`, {
      headers: { 'Content-Type': 'application/json', 'Cookie': cookie },
      data: {
        userId,
        clientId,
        installationName,
        ttlSeconds: '3600',
        scopes: ['workspace:read'],
      },
    })
    expect(res.status(), await res.text()).toBe(200)
  }

  /** Every live credential this session's account holds, by installation. */
  async function listInstallations(
    request: import('@playwright/test').APIRequestContext,
    hubUrl: string,
    cookie: string,
  ): Promise<string[]> {
    const res = await request.post(`${hubUrl}/leapmux.v1.UserService/ListMyAPITokens`, {
      headers: { 'Content-Type': 'application/json', 'Cookie': cookie },
      data: {},
    })
    expect(res.status(), await res.text()).toBe(200)
    const body = await res.json() as { tokens?: Array<{ installationName?: string }> }
    return (body.tokens ?? []).map(t => t.installationName ?? '')
  }

  // Disconnecting takes EVERY machine the app runs on, in one call.
  //
  // This is the assertion the whole grouping in Preferences rests on. An
  // ending that took one installation would leave the app working everywhere
  // else, which is exactly what somebody who stopped trusting an app is trying
  // to prevent -- and with a flat credential list they could not tell.
  test('kills every credential the app holds, and no other app\'s', async ({ page, leapmuxServer }) => {
    const hubUrl = leapmuxServer.hubUrl
    const adminCookie = await loginViaAPI(hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD)
    await elevateSessionViaAPI(hubUrl, adminCookie, TEST_ADMIN_PASSWORD)

    const me = await page.request.post(`${hubUrl}/leapmux.v1.AuthService/GetCurrentUser`, {
      headers: { 'Content-Type': 'application/json', 'Cookie': adminCookie },
      data: {},
    })
    const userId = ((await me.json()) as { user: { id: string } }).user.id

    const { clientId } = await registerApp(page.request, hubUrl, adminCookie, {
      clientName: 'Two-machine app',
      redirectUris: ['https://two.example.com/callback'],
      scopes: ['SCOPE_WORKSPACE_READ'],
      visibility: 'APP_VISIBILITY_HUB_WIDE',
      clientType: 'APP_CLIENT_TYPE_PUBLIC',
    })
    const other = await registerApp(page.request, hubUrl, adminCookie, {
      clientName: 'Untouched app',
      redirectUris: ['https://untouched.example.com/callback'],
      scopes: ['SCOPE_WORKSPACE_READ'],
      visibility: 'APP_VISIBILITY_HUB_WIDE',
      clientType: 'APP_CLIENT_TYPE_PUBLIC',
    })

    await issueCredential(page.request, hubUrl, adminCookie, userId, clientId, 'laptop')
    await issueCredential(page.request, hubUrl, adminCookie, userId, clientId, 'desktop')
    await issueCredential(page.request, hubUrl, adminCookie, userId, other.clientId, 'ci-runner')
    expect(await listInstallations(page.request, hubUrl, adminCookie))
      .toEqual(expect.arrayContaining(['laptop', 'desktop', 'ci-runner']))

    const disconnect = await page.request.post(`${hubUrl}/leapmux.v1.UserService/DisconnectApp`, {
      headers: { 'Content-Type': 'application/json', 'Cookie': adminCookie },
      data: { clientId },
    })
    expect(disconnect.status(), await disconnect.text()).toBe(200)
    expect((await disconnect.json() as { revokedCredentialCount?: string }).revokedCredentialCount)
      .toBe('2')

    const left = await listInstallations(page.request, hubUrl, adminCookie)
    expect(left).not.toContain('laptop')
    expect(left).not.toContain('desktop')
    expect(left).toContain('ci-runner')
  })
})

test.describe('open registration', () => {
  // RFC 7591 dynamic registration is OFF by default, and the default is the
  // decision: an anonymous caller who can create a registration can create a
  // row that appears on a consent screen, which is a phishing surface as much
  // as a convenience.
  test('is refused while the setting is off, and the metadata says so', async ({ page, leapmuxServer }) => {
    const hubUrl = leapmuxServer.hubUrl

    const res = await page.request.post(`${hubUrl}/oauth/register`, {
      headers: { 'Content-Type': 'application/json' },
      data: {
        client_name: 'Anonymous app',
        redirect_uris: ['https://anon.example.com/callback'],
      },
    })
    expect(res.status()).toBe(403)

    // The metadata document OMITS registration_endpoint while the setting is
    // off, so a conformant client library does not try and does not report the
    // refusal as a hub failure.
    const meta = await page.request.get(`${hubUrl}/.well-known/oauth-authorization-server`)
    expect(meta.status()).toBe(200)
    const doc = await meta.json() as Record<string, unknown>
    expect(doc.registration_endpoint).toBeUndefined()
    // The rest of the document is there, so the absence above is the setting
    // rather than a document that failed to build.
    expect(doc.authorization_endpoint).toBeTruthy()
    expect(doc.token_endpoint).toBeTruthy()
    expect(doc.code_challenge_methods_supported).toEqual(['S256'])
  })

  // An administrator flips the toggle and the SAME request succeeds.
  //
  // The refusal above alone would pass for a hub that never serves the
  // endpoint at all, so the setting has to be shown to be the whole gate --
  // and the metadata document has to start naming the endpoint in the same
  // breath, or a conformant library never finds it.
  test('succeeds once an administrator turns the setting on', async ({ page, leapmuxServer }) => {
    const hubUrl = leapmuxServer.hubUrl
    const adminCookie = await loginViaAPI(hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD)
    await elevateSessionViaAPI(hubUrl, adminCookie, TEST_ADMIN_PASSWORD)
    await updateSettingViaAPI(hubUrl, adminCookie, 'open_app_registration', 'true')

    const res = await page.request.post(`${hubUrl}/oauth/register`, {
      headers: { 'Content-Type': 'application/json' },
      data: {
        client_name: 'Anonymous app',
        redirect_uris: ['https://anon.example.com/callback'],
        scope: 'workspace:read',
      },
    })
    expect(res.status(), await res.text()).toBe(201)
    const registered = await res.json() as Record<string, unknown>
    expect(registered.client_id).toBeTruthy()
    // PUBLIC by default: a registrant who did not ask for a secret must not be
    // handed one and led to believe it is confidential.
    expect(registered.client_secret).toBeUndefined()
    expect(registered.token_endpoint_auth_method).toBe('none')

    const meta = await page.request.get(`${hubUrl}/.well-known/oauth-authorization-server`)
    const doc = await meta.json() as Record<string, unknown>
    expect(doc.registration_endpoint).toBe(`${hubUrl}/oauth/register`)

    // The registered app is authorizable, and the consent screen labels it
    // UNVERIFIED -- nobody vouched for something that registered itself.
    await loginViaToken(page, adminCookie)
    await page.goto(authorizeURL(hubUrl, registered.client_id as string, 'https://anon.example.com/callback'))
    await expect(page.getByRole('heading', { name: /unverified app/i })).toBeVisible()
  })
})
