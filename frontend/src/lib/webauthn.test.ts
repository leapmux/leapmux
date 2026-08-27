import type { PasskeyBlocker } from '~/lib/systemInfo'

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  encodeWebAuthnUserId,
  isPasskeyCeremonyCancelled,
  passkeyBlockerMessage,
  PasskeyCeremonyCancelledError,
  passkeyErrorMessage,
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
    const options = { challenge: 'abc', rp: { name: 'LeapMux' } }
    const result = await startRegistration(JSON.stringify(options))
    expect(mockStartRegistration).toHaveBeenCalledWith({ optionsJSON: options })
    expect(JSON.parse(result)).toEqual({ id: 'cred-1', response: {} })
  })

  it('unwraps go-webauthn publicKey envelopes before calling the browser', async () => {
    const inner = { challenge: 'abc', rp: { name: 'LeapMux' } }
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

describe('signal helpers', () => {
  const original = globalThis.PublicKeyCredential

  afterEach(() => {
    Object.defineProperty(globalThis, 'PublicKeyCredential', {
      configurable: true,
      value: original,
    })
  })

  it('encodeWebAuthnUserId base64url-encodes UTF-8 bytes of the UUID string', () => {
    // btoa("user-1") with URL-safe alphabet and no padding
    expect(encodeWebAuthnUserId('user-1')).toBe('dXNlci0x')
  })

  it('signalPasskeyRemoved calls the browser API when present', () => {
    const signalUnknownCredential = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(globalThis, 'PublicKeyCredential', {
      configurable: true,
      value: { signalUnknownCredential },
    })
    signalPasskeyRemoved('example.com', 'cred-id')
    expect(signalUnknownCredential).toHaveBeenCalledWith({ rpId: 'example.com', credentialId: 'cred-id' })
  })

  it('signalAcceptedPasskeys calls signalAllAcceptedCredentials when present', () => {
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

  it('signalAcceptedPasskeys skips empty credential lists', () => {
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
