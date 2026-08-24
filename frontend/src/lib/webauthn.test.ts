import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  encodeWebAuthnUserId,
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
