import type { DescMethod, JsonValue } from '@bufbuild/protobuf'
import type { Page } from '@playwright/test'
import { fromJson } from '@bufbuild/protobuf'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AuthService } from '../../../src/generated/proto/leapmux/v1/auth_pb'
import { UserService } from '../../../src/generated/proto/leapmux/v1/user_pb'
import { procedureOf } from '../../../src/lib/rpcProcedure'
import { addPasskeyViaAPIInBrowser, loginWithPasskeyViaAPIInBrowser, signUpWithPasskeyViaAPIInBrowser } from './webauthn'

vi.mock('./altcha', () => ({
  solveCaptchaViaAPI: vi.fn(async () => ({ payload: 'solved-captcha', honeypot: '' })),
}))
vi.mock('./api', () => ({
  elevateSessionViaAPI: vi.fn(async () => {}),
}))
vi.mock('./ui', () => ({
  loginViaToken: vi.fn(async () => {}),
  readSessionCookie: vi.fn(async () => 'leapmux-session=passkey'),
}))

const hubUrl = 'http://webauthn-hub.test'

/** The credential that the fake browser ceremony returns. */
const credential = { id: 'credential-1', type: 'public-key' }

/** The ceremonies of the fake SimpleWebAuthn bundle. */
const ceremonies = {
  startRegistration: vi.fn(async (_args: { optionsJSON: unknown }) => credential),
  startAuthentication: vi.fn(async (_args: { optionsJSON: unknown }) => credential),
}

/** A page that runs each `evaluate` body in this process, as the browser would run it. */
function fakePage(): Page {
  return {
    goto: vi.fn(async () => null),
    evaluate: async (run: (argument: unknown) => unknown, argument: unknown) => run(argument),
  } as unknown as Page
}

/**
 * Answer each Begin procedure with `begin` and each Finish procedure with an empty message, and record each request.
 * `begin` must decode as the output of `beginMethod`, so the fixture holds only a shape that the hub sends.
 */
function hub(beginMethod: DescMethod, begin: JsonValue) {
  fromJson(beginMethod.output, begin)
  const requests: Array<{ url: string, body: JsonValue }> = []
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url = String(input)
    requests.push({
      url,
      body: JSON.parse(String(init?.body)) as JsonValue,
    })
    return url.endsWith(procedureOf(beginMethod))
      ? Response.json(begin)
      : Response.json({})
  }))
  return requests
}

/**
 * Require that a recorded request travels to the procedure of `method`, and that its body decodes as the input of
 * `method`. The decode refuses an unknown field, so a body field that the proto no longer declares fails here.
 */
function expectRequest(request: { url: string, body: JsonValue } | undefined, method: DescMethod) {
  expect(request?.url).toBe(`${hubUrl}${procedureOf(method)}`)
  expect(() => fromJson(method.input, request?.body ?? null)).not.toThrow()
}

const challenge = {
  sessionId: 'ceremony-1',
  optionsJson: JSON.stringify({
    publicKey: { challenge: 'challenge-bytes' },
  }),
}

beforeEach(() => {
  ceremonies.startRegistration.mockClear()
  ceremonies.startAuthentication.mockClear()
  vi.stubGlobal('SimpleWebAuthnBrowser', ceremonies)
  // The real script unwraps go-webauthn's `{ publicKey: … }` wrapper in the same way.
  vi.stubGlobal('__unwrapWebAuthnOptions', (optionsJson: string) => (JSON.parse(optionsJson) as { publicKey: unknown }).publicKey)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('loginWithPasskeyViaAPIInBrowser', () => {
  it('sends the captcha proof to Begin, and the ceremony credential to Finish in one nested field', async () => {
    const requests = hub(AuthService.method.beginPasskeyLogin, { challenge })
    await expect(loginWithPasskeyViaAPIInBrowser(fakePage(), hubUrl, 'alice')).resolves.toBe('leapmux-session=passkey')
    expect(requests).toHaveLength(2)
    expectRequest(requests[0], AuthService.method.beginPasskeyLogin)
    expect(requests[0]?.body).toEqual({
      username: 'alice',
      captcha: { payload: 'solved-captcha', honeypot: '' },
    })
    expect(ceremonies.startAuthentication).toHaveBeenCalledExactlyOnceWith({
      optionsJSON: { challenge: 'challenge-bytes' },
    })
    expectRequest(requests[1], AuthService.method.finishPasskeyLogin)
    expect(requests[1]?.body).toEqual({
      credential: {
        sessionId: 'ceremony-1',
        credentialJson: JSON.stringify(credential),
      },
    })
  })

  it.each([
    { label: 'no challenge', begin: {} },
    {
      label: 'a challenge with no options',
      begin: {
        challenge: { sessionId: 'ceremony-1' },
      },
    },
    {
      label: 'a challenge with no session',
      begin: {
        challenge: { optionsJson: challenge.optionsJson },
      },
    },
  ])('refuses a Begin reply with $label, and runs no ceremony and no Finish', async ({ begin }) => {
    const requests = hub(AuthService.method.beginPasskeyLogin, begin)
    await expect(loginWithPasskeyViaAPIInBrowser(fakePage(), hubUrl, 'alice')).rejects.toThrow('AuthService/BeginPasskeyLogin returned incomplete payload')
    expect(requests).toHaveLength(1)
    expect(ceremonies.startAuthentication).not.toHaveBeenCalled()
  })
})

describe('signUpWithPasskeyViaAPIInBrowser', () => {
  it('sends the account fields and the captcha proof to Begin, and registers the ceremony credential', async () => {
    const requests = hub(AuthService.method.beginPasskeySignUp, { challenge })
    await expect(signUpWithPasskeyViaAPIInBrowser(fakePage(), hubUrl, 'bob', 'bob@test.local', 'Bob')).resolves.toBe('leapmux-session=passkey')
    expect(requests).toHaveLength(2)
    expectRequest(requests[0], AuthService.method.beginPasskeySignUp)
    expect(requests[0]?.body).toEqual({
      username: 'bob',
      displayName: 'Bob',
      email: 'bob@test.local',
      captcha: { payload: 'solved-captcha', honeypot: '' },
    })
    expect(ceremonies.startRegistration).toHaveBeenCalledExactlyOnceWith({
      optionsJSON: { challenge: 'challenge-bytes' },
    })
    expectRequest(requests[1], AuthService.method.finishPasskeySignUp)
    expect(requests[1]?.body).toEqual({
      credential: {
        sessionId: 'ceremony-1',
        credentialJson: JSON.stringify(credential),
      },
    })
  })
})

describe('addPasskeyViaAPIInBrowser', () => {
  it('sends the friendly name beside the nested credential to Finish', async () => {
    const requests = hub(UserService.method.beginPasskeyRegistration, { challenge })
    await addPasskeyViaAPIInBrowser(fakePage(), hubUrl, 'leapmux-session=owner', 'secret')
    expect(requests).toHaveLength(2)
    expectRequest(requests[0], UserService.method.beginPasskeyRegistration)
    expect(requests[0]?.body).toEqual({})
    expectRequest(requests[1], UserService.method.finishPasskeyRegistration)
    expect(requests[1]?.body).toEqual({
      credential: {
        sessionId: 'ceremony-1',
        credentialJson: JSON.stringify(credential),
      },
      friendlyName: 'E2E Passkey',
    })
  })
})
