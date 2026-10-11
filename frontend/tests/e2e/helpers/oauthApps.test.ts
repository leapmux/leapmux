import { beforeEach, describe, expect, it, vi } from 'vitest'
import { callHub } from './api'
import {
  authorizeParams,
  authorizeURL,
  consentForm,
  CONTROL_CLI_CLIENT_ID,
  listAppsViaAPI,
  LOOPBACK_REDIRECT_URI,
  registerAppViaAPI,
  TEST_PKCE_CHALLENGE,
} from './oauthApps'

vi.mock('./api', () => ({ callHub: vi.fn() }))

beforeEach(() => {
  vi.mocked(callHub).mockReset()
})

describe('authorizeParams', () => {
  it('asks for a code for the control CLI on its loopback callback, with an S256 challenge', () => {
    expect(authorizeParams()).toEqual({
      client_id: CONTROL_CLI_CLIENT_ID,
      response_type: 'code',
      code_challenge_method: 'S256',
      redirect_uri: LOOPBACK_REDIRECT_URI,
      state: 'state-e2e',
      code_challenge: TEST_PKCE_CHALLENGE,
      installation_name: 'e2e-laptop',
    })
  })

  it('takes the app, the callback, and the state of the request', () => {
    expect(authorizeParams({ clientId: 'app-1', redirectUri: 'https://app.example.com/cb', state: 'state-9' })).toMatchObject({
      client_id: 'app-1',
      redirect_uri: 'https://app.example.com/cb',
      state: 'state-9',
    })
  })

  it('adds the extra parameters, and an extra parameter replaces a default', () => {
    const params = authorizeParams({ extra: { scope: 'admin:read', installation_name: 'ci' } })
    expect(params.scope).toBe('admin:read')
    expect(params.installation_name).toBe('ci')
  })
})

describe('authorizeURL', () => {
  it('puts the encoded parameters on the authorize path of the hub', () => {
    const url = new URL(authorizeURL('http://hub.test', { extra: { scope: 'admin:read admin:users' } }))
    expect(url.origin + url.pathname).toBe('http://hub.test/oauth/authorize')
    expect(url.searchParams.get('scope')).toBe('admin:read admin:users')
    expect(url.searchParams.get('code_challenge')).toBe(TEST_PKCE_CHALLENGE)
    expect(url.searchParams.get('redirect_uri')).toBe(LOOPBACK_REDIRECT_URI)
  })
})

describe('consentForm', () => {
  it.each(['allow', 'deny'] as const)('answers the request with %s, and keeps every authorization parameter', (decision) => {
    expect(consentForm({ state: 'state-deny' }, decision)).toEqual({ ...authorizeParams({ state: 'state-deny' }), decision })
  })
})

describe('registerAppViaAPI', () => {
  it('sends the body of the caller as the session, and returns the client ID and the secret', async () => {
    vi.mocked(callHub).mockResolvedValue({ app: { clientId: 'c-1' }, clientSecret: 's-1' })
    const body = { clientName: 'App', visibility: 'APP_VISIBILITY_PRIVATE' }
    await expect(registerAppViaAPI('http://hub.test', 'leapmux-session=a', body)).resolves.toEqual({ clientId: 'c-1', clientSecret: 's-1' })
    expect(callHub).toHaveBeenCalledWith('http://hub.test', 'AppService/RegisterApp', body, { cookie: 'leapmux-session=a', operation: 'registerAppViaAPI' })
  })

  it('reads a public client, which has no secret, as the empty secret', async () => {
    vi.mocked(callHub).mockResolvedValue({ app: { clientId: 'c-2' } })
    await expect(registerAppViaAPI('http://hub.test', 'c', {})).resolves.toEqual({ clientId: 'c-2', clientSecret: '' })
  })

  it('refuses a reply with no client ID', async () => {
    vi.mocked(callHub).mockResolvedValue({ app: {} })
    await expect(registerAppViaAPI('http://hub.test', 'c', {})).rejects.toThrow('no client ID')
  })
})

describe('listAppsViaAPI', () => {
  it('returns the client ID of each app', async () => {
    vi.mocked(callHub).mockResolvedValue({ apps: [{ clientId: 'c-1' }, { clientId: 'c-2' }] })
    await expect(listAppsViaAPI('http://hub.test', 'c')).resolves.toEqual(['c-1', 'c-2'])
  })

  it('reads an absent list as no app', async () => {
    vi.mocked(callHub).mockResolvedValue({})
    await expect(listAppsViaAPI('http://hub.test', 'c')).resolves.toEqual([])
  })

  it('refuses an app with no client ID', async () => {
    vi.mocked(callHub).mockResolvedValue({ apps: [{}] })
    await expect(listAppsViaAPI('http://hub.test', 'c')).rejects.toThrow('no client ID')
  })
})
