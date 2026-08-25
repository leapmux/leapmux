import { Code, ConnectError } from '@connectrpc/connect'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import {
  credentialIdFromRegistrationJson,
  isReauthProofRejected,
  loadPasskeys,
  obtainPasskeyReauthProof,
} from './passkeyManagement'

const mockBeginPasskeyReauth = vi.fn()
const mockFinishPasskeyReauth = vi.fn()
const mockListPasskeys = vi.fn()
const mockStartAuthentication = vi.fn()

vi.mock('~/api/clients', () => ({
  userClient: {
    beginPasskeyReauth: (...args: unknown[]) => mockBeginPasskeyReauth(...args),
    finishPasskeyReauth: (...args: unknown[]) => mockFinishPasskeyReauth(...args),
    listPasskeys: (...args: unknown[]) => mockListPasskeys(...args),
  },
}))

vi.mock('~/lib/webauthn', () => ({
  startAuthentication: (...args: unknown[]) => mockStartAuthentication(...args),
}))

describe('passkeyManagement helpers', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('credentialIdFromRegistrationJson reads the credential id', () => {
    expect(credentialIdFromRegistrationJson('{"id":"abc"}')).toBe('abc')
    expect(credentialIdFromRegistrationJson('not-json')).toBeUndefined()
  })

  it('isReauthProofRejected matches only Unauthenticated connect errors', () => {
    expect(isReauthProofRejected(new ConnectError('reauth proof rejected', Code.Unauthenticated))).toBe(true)
    expect(isReauthProofRejected(new ConnectError('gone', Code.NotFound))).toBe(false)
    expect(isReauthProofRejected(new Error('not a connect error'))).toBe(false)
    expect(isReauthProofRejected(undefined)).toBe(false)
  })

  it('obtainPasskeyReauthProof chains begin, authenticate, and finish', async () => {
    mockBeginPasskeyReauth.mockResolvedValue({ sessionId: 'sess-1', optionsJson: '{"challenge":"c"}' })
    mockStartAuthentication.mockResolvedValue('{"id":"cred-1"}')
    mockFinishPasskeyReauth.mockResolvedValue({ reauthProof: 'proof-1' })

    await expect(obtainPasskeyReauthProof()).resolves.toBe('proof-1')
    expect(mockStartAuthentication).toHaveBeenCalledWith('{"challenge":"c"}')
    expect(mockFinishPasskeyReauth).toHaveBeenCalledWith({
      sessionId: 'sess-1',
      credentialJson: '{"id":"cred-1"}',
    })
  })

  it('loadPasskeys returns the list with the server-derived RP ID', async () => {
    const passkeys = [{ id: 'pk-1', friendlyName: 'Laptop', credentialId: 'cred-abc' }]
    mockListPasskeys.mockResolvedValue({ passkeys, rpId: 'example.com' })

    await expect(loadPasskeys()).resolves.toEqual({ passkeys, rpId: 'example.com' })
  })
})
