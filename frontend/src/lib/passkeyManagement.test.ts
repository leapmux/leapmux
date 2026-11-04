import { beforeEach, describe, expect, it, vi } from 'vitest'

import {
  credentialIdFromRegistrationJson,
  loadPasskeys,
} from './passkeyManagement'

const mockListPasskeys = vi.fn()

vi.mock('~/api/clients', () => ({
  userClient: {
    listPasskeys: (...args: unknown[]) => mockListPasskeys(...args),
  },
}))

describe('passkeyManagement helpers', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('reads the credential id in credentialIdFromRegistrationJson', () => {
    expect(credentialIdFromRegistrationJson('{"id":"abc"}')).toBe('abc')
    expect(credentialIdFromRegistrationJson('not-json')).toBeUndefined()
  })

  it('ignores a non-string id in credentialIdFromRegistrationJson', () => {
    expect(credentialIdFromRegistrationJson('{"id":42}')).toBeUndefined()
    expect(credentialIdFromRegistrationJson('{}')).toBeUndefined()
  })

  it('returns the list with the server-derived RP ID from loadPasskeys', async () => {
    const passkeys = [
      { id: 'pk-1', friendlyName: 'Laptop', credentialId: 'cred-abc' },
    ]
    mockListPasskeys.mockResolvedValue({ passkeys, rpId: 'example.com' })

    await expect(loadPasskeys()).resolves.toEqual({ passkeys, rpId: 'example.com' })
  })
})
