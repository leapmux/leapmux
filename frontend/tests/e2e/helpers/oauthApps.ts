import { callHub } from './api'

/**
 * OAuth app fixtures for the specs of app registration and consent: the authorization request that a test app makes,
 * the consent form that answers it, and the AppService calls that register and list apps.
 */

/**
 * The client ID of the built-in control CLI. It is the one app that a fresh hub ships with, its redirect address is a
 * constant of the build, and the build vouches for it, so its consent page is the VERIFIED branch.
 */
export const CONTROL_CLI_CLIENT_ID = 'leapmux-control-cli'

/** A syntactically valid S256 PKCE challenge: the example of RFC 7636, appendix B. */
export const TEST_PKCE_CHALLENGE = 'E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM'
export const TEST_PKCE_VERIFIER = 'dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk'

/** The loopback callback of a native test app. RFC 8252 lets such an app bind any port. */
export const LOOPBACK_REDIRECT_URI = 'http://127.0.0.1:54321/callback'

/** One authorization request of a test app. */
export interface AuthorizeRequest {
  /** The app. The default is the built-in control CLI. */
  clientId?: string
  /** The registered callback of the app. The default is {@link LOOPBACK_REDIRECT_URI}. */
  redirectUri?: string
  /** The state that the hub returns to the app. The default is `state-e2e`. */
  state?: string
  /** The step-up target: `session:<id>` or `token:<id>`. */
  stepUp?: string
  /** More parameters, such as `scope`. A parameter here replaces a default of the same name. */
  extra?: Record<string, string>
}

/** The parameters of an authorization request, which the authorize URL and the consent form both carry. */
export function authorizeParams(request: AuthorizeRequest = {}): Record<string, string> {
  const params: Record<string, string> = {
    client_id: request.clientId ?? CONTROL_CLI_CLIENT_ID,
    response_type: 'code',
    code_challenge_method: 'S256',
    redirect_uri: request.redirectUri ?? LOOPBACK_REDIRECT_URI,
    state: request.state ?? 'state-e2e',
    code_challenge: TEST_PKCE_CHALLENGE,
    installation_name: 'e2e-laptop',
    ...request.extra,
  }
  if (request.stepUp !== undefined)
    params.step_up = request.stepUp
  return params
}

/** The authorization URL of the hub for one request. */
export function authorizeURL(hubUrl: string, request: AuthorizeRequest = {}): string {
  return `${hubUrl}/oauth/authorize?${new URLSearchParams(authorizeParams(request)).toString()}`
}

/** The body of a consent POST that answers one request with `decision`. */
export function consentForm(request: AuthorizeRequest, decision: 'allow' | 'deny'): Record<string, string> {
  return {
    ...authorizeParams(request),
    decision,
  }
}

/** The app that a registration created, and its secret: the empty string for a public client, which has none. */
export interface RegisteredApp {
  clientId: string
  clientSecret: string
}

/**
 * Register an app through AppService as the session `cookie`, which must be elevated.
 * The caller writes the whole body, so each test states the visibility, the client type, and the scopes it tests.
 */
export async function registerAppViaAPI(hubUrl: string, cookie: string, body: Record<string, unknown>): Promise<RegisteredApp> {
  const data = await callHub<{ app?: { clientId?: string }, clientSecret?: string }>(
    hubUrl,
    'AppService/RegisterApp',
    body,
    { cookie, operation: 'registerAppViaAPI' },
  )
  if (!data.app?.clientId)
    throw new Error('registerAppViaAPI: the RegisterApp reply holds no client ID.')
  return { clientId: data.app.clientId, clientSecret: data.clientSecret ?? '' }
}

/** The client IDs of the apps that the session `cookie` may edit. A hub-wide app of another owner is not one of them. */
export async function listAppsViaAPI(hubUrl: string, cookie: string): Promise<string[]> {
  const data = await callHub<{ apps?: Array<{ clientId?: string }> }>(hubUrl, 'AppService/ListApps', {}, { cookie, operation: 'listAppsViaAPI' })
  return (data.apps ?? []).map((app) => {
    if (!app.clientId)
      throw new Error('listAppsViaAPI: the ListApps reply holds an app with no client ID.')
    return app.clientId
  })
}

/**
 * Issue a control-CLI credential through the authorization-code exchange.
 * The caller passes the cookie of an ELEVATED session (consent requires one).
 */
export async function issueControlCLICredentialViaAPI(
  hubUrl: string,
  cookie: string,
): Promise<{ tokenId: string }> {
  const { fetchHub } = await import('./hubTransport')

  // 1. POST the consent form with "allow" and read the code from Location.
  const consentRes = await fetchHub(`${hubUrl}/oauth/consent`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/x-www-form-urlencoded',
      'Cookie': cookie,
    },
    body: new URLSearchParams(consentForm({}, 'allow')).toString(),
    redirect: 'manual',
  })
  const location = consentRes.headers.get('Location')
  if (!location)
    throw new Error(`issueControlCLICredentialViaAPI: no Location header (${consentRes.status})`)
  const code = new URL(location, hubUrl).searchParams.get('code')
  if (!code)
    throw new Error('issueControlCLICredentialViaAPI: no code in the redirect')

  // 2. POST the code exchange.
  const tokenRes = await fetchHub(`${hubUrl}/oauth/token`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({
      grant_type: 'authorization_code',
      client_id: CONTROL_CLI_CLIENT_ID,
      code,
      code_verifier: TEST_PKCE_VERIFIER,
      redirect_uri: LOOPBACK_REDIRECT_URI,
    }).toString(),
    redirect: 'manual',
  })
  const body = await tokenRes.json() as { token_id?: string }
  if (!body.token_id)
    throw new Error(`issueControlCLICredentialViaAPI: no token_id (${tokenRes.status})`)
  return { tokenId: body.token_id }
}
