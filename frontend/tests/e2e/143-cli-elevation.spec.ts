/**
 * The CLI consent gate and the /elevate bounce.
 *
 * A unit test cannot catch what this spec exists for. The hub sends the
 * browser to `/elevate?redirect=/auth/cli/start...`, and `/auth/cli/start`
 * is a Go mux route, not an entry in this application's route table -- so a
 * client-side `navigate()` back to it renders the SPA's 404 page while the
 * CLI waits for a consent screen nobody ever sees. Only a real browser that
 * walks the whole round trip shows that.
 */

import { expect, test } from './fixtures'
import { elevateSessionViaAPI, loginViaAPI, signUpViaAPI, TEST_ADMIN_PASSWORD, TEST_ADMIN_USERNAME } from './helpers/api'
import { loginViaToken } from './helpers/ui'

/** A syntactically valid PKCE consent URL for a loopback CLI listener. */
function startURL(hubUrl: string, extra: Record<string, string> = {}): string {
  const params = new URLSearchParams({
    redirect_uri: 'http://127.0.0.1:54321/callback',
    state: `state-${Date.now()}`,
    code_challenge: 'E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM',
    device_name: 'e2e-laptop',
    ...extra,
  })
  return `${hubUrl}/auth/cli/start?${params.toString()}`
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

test.describe('CLI consent needs an elevated session', () => {
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
    await expect(page).toHaveURL(/\/auth\/cli\/start\?/)
    await expect(page.getByRole('button', { name: 'Allow' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Authorize CLI access?' })).toBeVisible()
  })

  test('an already-elevated session reaches the consent page directly', async ({ page, leapmuxServer }) => {
    const cookie = await freshAdminSession(leapmuxServer.hubUrl)
    await elevateSessionViaAPI(leapmuxServer.hubUrl, cookie, TEST_ADMIN_PASSWORD)
    await loginViaToken(page, cookie)

    await page.goto(startURL(leapmuxServer.hubUrl))

    await expect(page).toHaveURL(/\/auth\/cli\/start\?/)
    await expect(page.getByRole('button', { name: 'Allow' })).toBeVisible()
  })

  test('the elevated marker stops the second bounce instead of admitting', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, await freshAdminSession(leapmuxServer.hubUrl))

    // The marker is what the hub itself appends to the return address. A
    // request that carries it and is STILL not elevated must get an
    // explanation, not another redirect -- and must not be admitted.
    await page.goto(startURL(leapmuxServer.hubUrl, { elevated: '1' }))

    await expect(page).toHaveURL(/\/auth\/cli\/start\?/)
    await expect(page.getByRole('heading', { name: 'Verify your identity' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Allow' })).toHaveCount(0)
  })

  test('refuses the consent POST rather than redirecting it', async ({ page, leapmuxServer }) => {
    // The POST carries the PKCE challenge in its BODY, so a redirect would
    // destroy the flow irrecoverably. The hub refuses it instead.
    const cookie = await freshAdminSession(leapmuxServer.hubUrl)
    const res = await page.request.post(`${leapmuxServer.hubUrl}/auth/cli/authorize`, {
      headers: { 'Content-Type': 'application/x-www-form-urlencoded', 'Cookie': cookie },
      form: {
        redirect_uri: 'http://127.0.0.1:54321/callback',
        state: 'state-post',
        code_challenge: 'E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM',
        device_name: 'e2e-laptop',
      },
      maxRedirects: 0,
    })
    expect(res.status()).toBe(403)
    expect(await res.text()).toContain('Verify your identity')
  })

  test('refuses --admin from an account that is not an administrator', async ({ page, leapmuxServer }) => {
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

    const res = await page.request.get(startURL(leapmuxServer.hubUrl, { admin: '1' }), {
      headers: { Cookie: cookie },
      maxRedirects: 0,
    })
    expect(res.status()).toBe(403)
    expect(await res.text()).toContain('not a hub administrator')
  })

  test('states the admin grant on the consent page for an administrator', async ({ page, leapmuxServer }) => {
    const cookie = await freshAdminSession(leapmuxServer.hubUrl)
    await elevateSessionViaAPI(leapmuxServer.hubUrl, cookie, TEST_ADMIN_PASSWORD)
    await loginViaToken(page, cookie)

    await page.goto(startURL(leapmuxServer.hubUrl, { admin: '1' }))

    // The browser is where the consent happens, so the page -- not only the
    // CLI that asked -- says what the credential will be able to do.
    await expect(page.getByText('administer the hub')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Allow' })).toBeVisible()
  })
})
