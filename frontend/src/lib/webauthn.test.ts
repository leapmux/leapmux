import type { WebAuthnChallenge } from '~/generated/proto/leapmux/v1/auth_pb'
import type { PasskeyBlocker } from '~/lib/systemInfo'

import { create } from '@bufbuild/protobuf'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { WebAuthnChallengeSchema } from '~/generated/proto/leapmux/v1/auth_pb'
import {
  authenticateForChallenge,
  encodeWebAuthnUserId,
  isPasskeyCeremonyCancelled,
  passkeyBlockerMessage,
  PasskeyCeremonyCancelledError,
  passkeyErrorMessage,
  registerForChallenge,
  signalAcceptedPasskeys,
  signalPasskeyRemoved,
  startAuthentication,
  startRegistration,
} from './webauthn'

const mockStartRegistration = vi.fn()
const mockStartAuthentication = vi.fn()

vi.mock('@simplewebauthn/browser', () => ({
  startRegistration: (...args: unknown[]) => mockStartRegistration(...args),
  startAuthentication: (...args: unknown[]) => mockStartAuthentication(...args),
}))

describe('startRegistration', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockStartRegistration.mockResolvedValue({ id: 'cred-1', response: {} })
  })

  it('parses server JSON and returns a credential JSON string', async () => {
    const options = {
      challenge: 'abc',
      rp: { name: 'LeapMux' },
    }
    const result = await startRegistration(JSON.stringify(options))
    expect(mockStartRegistration).toHaveBeenCalledWith({ optionsJSON: options })
    expect(JSON.parse(result)).toEqual({ id: 'cred-1', response: {} })
  })

  it('unwraps go-webauthn publicKey envelopes before calling the browser', async () => {
    const inner = {
      challenge: 'abc',
      rp: { name: 'LeapMux' },
    }
    await startRegistration(JSON.stringify({ publicKey: inner }))
    expect(mockStartRegistration).toHaveBeenCalledWith({ optionsJSON: inner })
  })
})

describe('startAuthentication', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockStartAuthentication.mockResolvedValue({ id: 'cred-2', response: {} })
  })

  it('parses server JSON and returns a credential JSON string', async () => {
    const options = { challenge: 'xyz', rpId: 'localhost' }
    const result = await startAuthentication(JSON.stringify(options))
    expect(mockStartAuthentication).toHaveBeenCalledWith({ optionsJSON: options })
    expect(JSON.parse(result)).toEqual({ id: 'cred-2', response: {} })
  })

  it('unwraps go-webauthn publicKey envelopes before calling the browser', async () => {
    const inner = { challenge: 'xyz', rpId: 'localhost' }
    await startAuthentication(JSON.stringify({ publicKey: inner }))
    expect(mockStartAuthentication).toHaveBeenCalledWith({ optionsJSON: inner })
  })
})

/** What a person reads when a Begin response carries no challenge. */
const MISSING_CHALLENGE_TEXT = 'The hub could not start the passkey prompt. Try again.'

/** A Begin response's challenge, as the hub sends it. */
function challengeOf(sessionId: string, options: Record<string, unknown>): WebAuthnChallenge {
  return create(WebAuthnChallengeSchema, {
    sessionId,
    optionsJson: JSON.stringify(options),
  })
}

// One helper per ceremony pairs a challenge's session with the credential the
// browser returned for that same challenge, so no caller can mix two.
describe('registerForChallenge', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockStartRegistration.mockResolvedValue({ id: 'cred-1', response: {} })
  })

  it('pairs the challenge\'s session with the credential the browser returned', async () => {
    const options = {
      challenge: 'abc',
      rp: { name: 'LeapMux' },
    }

    const credential = await registerForChallenge(challengeOf('sess-reg', options))

    expect(mockStartRegistration).toHaveBeenCalledWith({ optionsJSON: options })
    expect(credential.sessionId).toBe('sess-reg')
    expect(JSON.parse(credential.credentialJson ?? '')).toEqual({ id: 'cred-1', response: {} })
  })

  // A person reads the refusal through passkeyErrorMessage on every passkey
  // surface, so it states the fault in plain words and offers a next step.
  it('refuses an absent challenge without opening the browser prompt', async () => {
    const err = await registerForChallenge(undefined).catch((e: unknown) => e)

    expect(passkeyErrorMessage(err, 'Sign up failed')).toBe(MISSING_CHALLENGE_TEXT)
    expect(mockStartRegistration).not.toHaveBeenCalled()
  })

  it('passes a dismissed prompt through as a cancellation', async () => {
    mockStartRegistration.mockRejectedValue(Object.assign(new Error('aborted'), { code: 'ERROR_CEREMONY_ABORTED' }))

    const err = await registerForChallenge(challengeOf('sess-reg', { challenge: 'abc' })).catch((e: unknown) => e)

    expect(isPasskeyCeremonyCancelled(err)).toBe(true)
  })
})

describe('authenticateForChallenge', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockStartAuthentication.mockResolvedValue({ id: 'cred-2', response: {} })
  })

  it('pairs the challenge\'s session with the credential the browser returned', async () => {
    const options = { challenge: 'xyz', rpId: 'localhost' }

    const credential = await authenticateForChallenge(challengeOf('sess-auth', options))

    expect(mockStartAuthentication).toHaveBeenCalledWith({ optionsJSON: options })
    expect(mockStartRegistration).not.toHaveBeenCalled()
    expect(credential.sessionId).toBe('sess-auth')
    expect(JSON.parse(credential.credentialJson ?? '')).toEqual({ id: 'cred-2', response: {} })
  })

  it('refuses an absent challenge without opening the browser prompt', async () => {
    const err = await authenticateForChallenge(undefined).catch((e: unknown) => e)

    expect(passkeyErrorMessage(err, 'Passkey login failed')).toBe(MISSING_CHALLENGE_TEXT)
    expect(mockStartAuthentication).not.toHaveBeenCalled()
  })

  it('rethrows a real browser failure unchanged', async () => {
    const failure = new Error('no authenticator')
    mockStartAuthentication.mockRejectedValue(failure)

    await expect(authenticateForChallenge(challengeOf('sess-auth', { challenge: 'xyz' }))).rejects.toBe(failure)
  })
})

describe('signal helpers', () => {
  const original = globalThis.PublicKeyCredential

  afterEach(() => {
    Object.defineProperty(globalThis, 'PublicKeyCredential', {
      configurable: true,
      value: original,
    })
  })

  it('base64url-encodes the UTF-8 bytes of the UUID string in encodeWebAuthnUserId', () => {
    // btoa("user-1") with URL-safe alphabet and no padding
    expect(encodeWebAuthnUserId('user-1')).toBe('dXNlci0x')
  })

  it('calls the browser API from signalPasskeyRemoved when the API is present', () => {
    const signalUnknownCredential = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(globalThis, 'PublicKeyCredential', {
      configurable: true,
      value: { signalUnknownCredential },
    })
    signalPasskeyRemoved('example.com', 'cred-id')
    expect(signalUnknownCredential).toHaveBeenCalledWith({ rpId: 'example.com', credentialId: 'cred-id' })
  })

  it('calls signalAllAcceptedCredentials from signalAcceptedPasskeys when the browser API is present', () => {
    const signalAllAcceptedCredentials = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(globalThis, 'PublicKeyCredential', {
      configurable: true,
      value: { signalAllAcceptedCredentials },
    })
    signalAcceptedPasskeys('example.com', 'user-1', ['cred-a'])
    expect(signalAllAcceptedCredentials).toHaveBeenCalledWith({
      rpId: 'example.com',
      userId: encodeWebAuthnUserId('user-1'),
      allAcceptedCredentialIds: ['cred-a'],
    })
  })

  it('skips empty credential lists in signalAcceptedPasskeys', () => {
    const signalAllAcceptedCredentials = vi.fn()
    Object.defineProperty(globalThis, 'PublicKeyCredential', {
      configurable: true,
      value: { signalAllAcceptedCredentials },
    })
    signalAcceptedPasskeys('example.com', 'user-1', [])
    expect(signalAllAcceptedCredentials).not.toHaveBeenCalled()
  })
})

describe('passkey ceremony error classification', () => {
  // SimpleWebAuthn passes the browser's raw DOMException text through, so
  // an unclassified cancel put "The operation either timed out or was not
  // allowed. See: https://www.w3.org/TR/webauthn-2/..." in a red banner.
  it('classifies a dismissed prompt as cancelled, not an error', () => {
    const cancelled = new PasskeyCeremonyCancelledError(new Error('raw'))
    expect(isPasskeyCeremonyCancelled(cancelled)).toBe(true)
    expect(passkeyErrorMessage(cancelled, 'Failed to add passkey')).toBeNull()
  })

  it('keeps the raw error as the cause so the log still has it', () => {
    const raw = new Error('NotAllowedError')
    expect(new PasskeyCeremonyCancelledError(raw).cause).toBe(raw)
  })

  it('reports a real failure with the caller fallback', () => {
    const real = new Error('authenticator is unreachable')
    expect(isPasskeyCeremonyCancelled(real)).toBe(false)
    expect(passkeyErrorMessage(real, 'Failed to add passkey')).toBe('authenticator is unreachable')
  })

  it('falls back when the failure carries no message', () => {
    expect(passkeyErrorMessage({}, 'Failed to add passkey')).toBe('Failed to add passkey')
  })
})

/**
 * One text for each blocker, and each one identifies the party that has to act.
 *
 * The three remedies go to three different people, so a shared or missing
 * sentence is a defect the surfaces cannot catch: they render whatever this
 * returns. The copy this map replaced specified an administrator for every
 * blocker, which is wrong advice for the two blockers the browser raises.
 */
describe('passkeyBlockerMessage', () => {
  const blockers: PasskeyBlocker[] = ['insecure-context', 'no-webauthn', 'origin-not-allowed']

  it('gives each blocker its own non-empty sentence', () => {
    const texts = blockers.map(passkeyBlockerMessage)
    for (const text of texts)
      expect(text.length).toBeGreaterThan(0)
    expect(new Set(texts).size).toBe(blockers.length)
  })

  it('sends only the hub blocker to an administrator', () => {
    expect(passkeyBlockerMessage('origin-not-allowed')).toMatch(/administrator/i)
    expect(passkeyBlockerMessage('insecure-context')).not.toMatch(/administrator/i)
    expect(passkeyBlockerMessage('no-webauthn')).not.toMatch(/administrator/i)
  })

  it('states the repair each party can make', () => {
    expect(passkeyBlockerMessage('insecure-context')).toMatch(/HTTPS/)
    expect(passkeyBlockerMessage('no-webauthn')).toMatch(/browser/i)
    expect(passkeyBlockerMessage('origin-not-allowed')).toMatch(/address/i)
  })
})
