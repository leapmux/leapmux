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

import { expect } from '@playwright/test'
import { AppClientType, AppVisibility } from '../../src/generated/proto/leapmux/v1/app_pb'
import { Scope } from '../../src/generated/proto/leapmux/v1/scope_pb'
import { test } from './fixtures'
import { elevatedAdminSessionViaAPI, elevateSessionViaAPI, freshAdminSessionViaAPI, signUpViaAPI, TEST_ADMIN_PASSWORD } from './helpers/api'
import { authorizeURL, consentForm, LOOPBACK_REDIRECT_URI, registerAppViaAPI } from './helpers/oauthApps'
import { answerElevationPrompt, loginViaToken } from './helpers/ui'

/**
 * Register an UNVERIFIED app through the Connect RPC, as an elevated session.
 *
 * The built-in control CLI is verified BY CONSTRUCTION -- a registration
 * source the build itself stands behind -- so the consent page's unverified
 * branch, the branch this spec's heading assertions exist for, needs a
 * registration nobody vouched for. Registering one is also the honest shape
 * of the risk: open registration lets any caller be the registrant.
 */
async function registerUnverifiedApp(
  hubUrl: string,
  elevatedCookie: string,
  name: string,
  scopes: number[] = [Scope.WORKSPACE_READ],
): Promise<string> {
  const { clientId } = await registerAppViaAPI(hubUrl, elevatedCookie, {
    clientName: name,
    clientUri: 'https://example.com',
    redirectUris: [LOOPBACK_REDIRECT_URI],
    scopes,
    visibility: AppVisibility.PRIVATE,
    clientType: AppClientType.PUBLIC,
  })
  return clientId
}

test.describe('app consent needs an elevated session', () => {
  test('bounces through /elevate and returns to the consent page', async ({ page, leapmuxServer }) => {
    // The consent page renders its UNVERIFIED branch for a registered app;
    // the built-in CLI is verified by construction, so its heading carries
    // its own name instead.
    const registrar = await elevatedAdminSessionViaAPI(leapmuxServer.hubUrl)
    const clientId = await registerUnverifiedApp(leapmuxServer.hubUrl, registrar, 'Consent e2e app')

    // The BROWSER holds a SECOND, un-elevated session of the same account:
    // the bounce is the first half of what this test walks.
    await loginViaToken(page, await freshAdminSessionViaAPI(leapmuxServer.hubUrl))

    await page.goto(authorizeURL(leapmuxServer.hubUrl, { clientId }))

    // Layer one: the hub sends the un-elevated session to the SPA prompt,
    // with the consent URL as the return address: the standalone /elevate
    // page, not the dialog of the app.
    await expect(page).toHaveURL(/\/elevate\?redirect=/)
    await answerElevationPrompt(page, TEST_ADMIN_PASSWORD, page.getByTestId('elevate-card'))

    // Back on the HUB's page, not the SPA's 404. This is the assertion the
    // whole spec exists for.
    await expect(page).toHaveURL(/\/oauth\/authorize\?/)
    await expect(page.getByRole('button', { name: 'Allow' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Authorize an unverified app?' })).toBeVisible()
  })

  test('an already-elevated session reaches the consent page directly', async ({ page, leapmuxServer }) => {
    const cookie = await elevatedAdminSessionViaAPI(leapmuxServer.hubUrl)
    await loginViaToken(page, cookie)

    await page.goto(authorizeURL(leapmuxServer.hubUrl))

    await expect(page).toHaveURL(/\/oauth\/authorize\?/)
    await expect(page.getByRole('button', { name: 'Allow' })).toBeVisible()
  })

  test('the elevated marker stops the second bounce instead of admitting', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, await freshAdminSessionViaAPI(leapmuxServer.hubUrl))

    // The marker is what the hub itself appends to the return address. A
    // request that carries it and is STILL not elevated must get an
    // explanation, not another redirect -- and must not be admitted.
    await page.goto(authorizeURL(leapmuxServer.hubUrl, { extra: { elevated: '1' } }))

    await expect(page).toHaveURL(/\/oauth\/authorize\?/)
    await expect(page.getByRole('heading', { name: 'Verify your identity' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Allow' })).toHaveCount(0)
  })

  test('refuses the consent POST rather than redirecting it', async ({ page, leapmuxServer }) => {
    // The POST carries the PKCE challenge in its BODY, so a redirect would
    // destroy the flow irrecoverably. The hub refuses it instead.
    const cookie = await freshAdminSessionViaAPI(leapmuxServer.hubUrl)
    const res = await page.request.post(`${leapmuxServer.hubUrl}/oauth/consent`, {
      headers: { 'Content-Type': 'application/x-www-form-urlencoded', 'Cookie': cookie },
      form: consentForm({ state: 'state-post' }, 'allow'),
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

    const res = await page.request.get(authorizeURL(leapmuxServer.hubUrl, { extra: { scope: 'admin:read' } }), {
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
    const cookie = await elevatedAdminSessionViaAPI(leapmuxServer.hubUrl)
    await loginViaToken(page, cookie)

    await page.goto(authorizeURL(leapmuxServer.hubUrl, { extra: { scope: 'admin:read admin:users' } }))

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
    // The phishing shape, stated as the registration's own choice of name.
    const claimed = 'LeapMux Official Security Check'
    const cookie = await elevatedAdminSessionViaAPI(leapmuxServer.hubUrl)
    const clientId = await registerUnverifiedApp(leapmuxServer.hubUrl, cookie, claimed)
    await loginViaToken(page, cookie)

    await page.goto(authorizeURL(leapmuxServer.hubUrl, { clientId }))

    const heading = page.getByRole('heading', { name: 'Authorize an unverified app?' })
    await expect(heading).toBeVisible()
    await expect(heading).not.toContainText(claimed)
    await expect(page.getByText('Nobody verified this app on this hub')).toBeVisible()
  })

  // The permission catalogue: the whole grantable vocabulary grouped by
  // family, the asked-for permissions ticked, the rest dimmed. The four
  // assertions are the layout contract -- a narrow family once laid its one
  // row BESIDE its label, the ticked marks read as dimmed because disabled
  // inputs grey out, and long sentences started on a line of their own.
  test('renders the permission catalogue grouped by family', async ({ page, leapmuxServer }) => {
    const cookie = await elevatedAdminSessionViaAPI(leapmuxServer.hubUrl)
    // workspace:read + file:read, which the hub closes with worker:read --
    // three granted scopes across three families, each with an ungranted
    // sibling to dim.
    const clientId = await registerUnverifiedApp(leapmuxServer.hubUrl, cookie, 'Catalogue e2e app', [Scope.WORKSPACE_READ, Scope.FILE_READ])
    await loginViaToken(page, cookie)

    await page.goto(authorizeURL(leapmuxServer.hubUrl, { clientId }))

    // WIDE: the catalogue is the widest thing the hub renders.
    await expect(page.locator('main.wide')).toBeVisible()

    const scopes = page.locator('.scopes')
    // Ticked = exactly the closed grant: workspace:read, worker:read,
    // file:read. The ungranted siblings carry no tick.
    await expect(scopes.locator('li.granted')).toHaveCount(3)
    const workspaceWrite = scopes.locator('li.not-granted').filter({ hasText: 'workspace:write' })
    await expect(workspaceWrite).toHaveCount(1)
    // The tick is PAINTED in the primary colour -- asserted on the mark's
    // own background, because a computed accent-color proved nothing: the
    // browsers ignore it on the disabled native checkbox this replaced.
    await expect(scopes.locator('li.granted > .tick').first()).toHaveCSS('background-color', 'rgb(13, 148, 136)')

    // The family label stands on its OWN line: the File label ends above
    // where file:read begins.
    const fileLabel = scopes.locator('.scope-category', { hasText: 'File' })
    const fileToken = scopes.locator('.scope-token', { hasText: 'file:read' })
    const labelBox = await fileLabel.boundingBox()
    const tokenBox = await fileToken.boundingBox()
    expect(labelBox && tokenBox).toBeTruthy()
    expect(labelBox!.y + labelBox!.height).toBeLessThanOrEqual(tokenBox!.y + 1)

    // A sentence begins BESIDE its token, never on a line of its own.
    const sentenceBox = await page
      .locator('.scope-sentence', { hasText: 'Browse and read files on your machines.' })
      .boundingBox()
    expect(sentenceBox).toBeTruthy()
    expect(Math.abs(sentenceBox!.y - tokenBox!.y)).toBeLessThan(2)
  })

  // DENY returns to the app immediately, with the standard error.
  //
  // Without it the app waited out the whole code TTL to learn that a person
  // refused, and a user who clicks Deny got no sign that anything happened.
  test('deny returns access_denied to the app and mints no code', async ({ page, leapmuxServer }) => {
    const cookie = await elevatedAdminSessionViaAPI(leapmuxServer.hubUrl)

    const res = await page.request.post(`${leapmuxServer.hubUrl}/oauth/consent`, {
      headers: { 'Content-Type': 'application/x-www-form-urlencoded', 'Cookie': cookie },
      form: consentForm({ state: 'state-deny' }, 'deny'),
      maxRedirects: 0,
    })

    expect(res.status()).toBe(302)
    const location = res.headers().location ?? ''
    expect(location).toContain('error=access_denied')
    expect(location).toContain('state=state-deny')
    expect(location).not.toContain('code=')
  })
})
