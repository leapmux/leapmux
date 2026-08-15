/// <reference types="vitest/globals" />
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { fetchAltchaChallenge } from './altchaChallenge'

const mockGetAltchaChallenge = vi.fn()
vi.mock('~/api/clients', () => ({
  authClient: {
    // The wrapper defers the const access past vi.mock's hoisting.
    getAltchaChallenge: (...args: []) => mockGetAltchaChallenge(...args),
  },
}))

const mockEnsureAltchaSolver = vi.fn(() => Promise.resolve())
vi.mock('~/lib/altchaSolvers', () => ({
  ensureAltchaSolver: (...args: []) => mockEnsureAltchaSolver(...args),
}))

vi.mock('~/lib/systemInfo', () => ({
  getAltchaAlgorithm: () => 'PBKDF2/SHA-256',
}))

describe('fetchAltchaChallenge', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('parses the interchange blob and pre-warms the issued algorithm', async () => {
    mockGetAltchaChallenge.mockResolvedValue({
      challengeJson: JSON.stringify({
        parameters: { algorithm: 'SCRYPT', salt: 'abc' },
        signature: 'sig',
      }),
    })
    const challenge = await fetchAltchaChallenge()
    expect(challenge?.parameters?.algorithm).toBe('SCRYPT')
    // The advertised algorithm pre-warms first (worker download overlaps
    // the fetch); the challenge's own algorithm pre-warms after.
    expect(mockEnsureAltchaSolver).toHaveBeenCalledWith('PBKDF2/SHA-256')
    expect(mockEnsureAltchaSolver).toHaveBeenCalledWith('SCRYPT')
  })

  it('returns null for the empty blob (hub reports no challenge)', async () => {
    // JSON.parse("") throws; the stand-down must not turn into a load
    // error that dead-locks the form.
    mockGetAltchaChallenge.mockResolvedValue({ challengeJson: '' })
    await expect(fetchAltchaChallenge()).resolves.toBeNull()
  })

  it('propagates transport failures for the caller to surface', async () => {
    mockGetAltchaChallenge.mockRejectedValue(new Error('network'))
    await expect(fetchAltchaChallenge()).rejects.toThrow('network')
  })
})
