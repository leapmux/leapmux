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

  it('credentialIdFromRegistrationJson reads the credential id', () => {
    expect(credentialIdFromRegistrationJson('{"id":"abc"}')).toBe('abc')
    expect(credentialIdFromRegistrationJson('not-json')).toBeUndefined()
  })

  it('credentialIdFromRegistrationJson ignores a non-string id', () => {
    expect(credentialIdFromRegistrationJson('{"id":42}')).toBeUndefined()
    expect(credentialIdFromRegistrationJson('{}')).toBeUndefined()
  })

  it('loadPasskeys returns the list with the server-derived RP ID', async () => {
    const passkeys = [{ id: 'pk-1', friendlyName: 'Laptop', credentialId: 'cred-abc' }]
    mockListPasskeys.mockResolvedValue({ passkeys, rpId: 'example.com' })

    await expect(loadPasskeys()).resolves.toEqual({ passkeys, rpId: 'example.com' })
  })
})
