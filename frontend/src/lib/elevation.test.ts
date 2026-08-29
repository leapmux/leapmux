import { timestampFromDate } from '@bufbuild/protobuf/wkt'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import {
  dropElevation,
  elevateWithPasskey,
  elevateWithPassword,
  isElevationCurrent,
  oauthReauthUrl,
} from './elevation'

const mockElevateSession = vi.fn()
const mockBeginPasskeyElevation = vi.fn()
const mockFinishPasskeyElevation = vi.fn()
const mockDropElevation = vi.fn()
const mockStartAuthentication = vi.fn()

vi.mock('~/api/clients', () => ({
  userClient: {
    elevateSession: (...args: unknown[]) => mockElevateSession(...args),
    beginPasskeyElevation: (...args: unknown[]) => mockBeginPasskeyElevation(...args),
    finishPasskeyElevation: (...args: unknown[]) => mockFinishPasskeyElevation(...args),
    dropElevation: (...args: unknown[]) => mockDropElevation(...args),
  },
}))

vi.mock('~/lib/webauthn', () => ({
  startAuthentication: (...args: unknown[]) => mockStartAuthentication(...args),
}))

describe('isElevationCurrent', () => {
  const now = new Date('2026-01-01T12:00:00Z')

  it('is false without a deadline', () => {
    expect(isElevationCurrent(undefined, now)).toBe(false)
  })

  it('is true strictly before the deadline and false at it', () => {
    expect(isElevationCurrent(timestampFromDate(new Date('2026-01-01T12:00:01Z')), now)).toBe(true)
    // Exclusive upper bound, matching the hub's own predicate: a credential
    // is invalid AT the recorded instant, not one tick afterward.
    expect(isElevationCurrent(timestampFromDate(now), now)).toBe(false)
    expect(isElevationCurrent(timestampFromDate(new Date('2026-01-01T11:59:59Z')), now)).toBe(false)
  })
})

describe('elevation ceremonies', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('elevateWithPassword returns the new deadline', async () => {
    const until = timestampFromDate(new Date('2026-01-01T14:00:00Z'))
    mockElevateSession.mockResolvedValue({ elevationExpiresAt: until })
    await expect(elevateWithPassword('secret')).resolves.toBe(until)
    expect(mockElevateSession).toHaveBeenCalledWith({ currentPassword: 'secret' })
  })

  it('elevateWithPasskey chains begin, the browser prompt, and finish', async () => {
    const until = timestampFromDate(new Date('2026-01-01T14:00:00Z'))
    mockBeginPasskeyElevation.mockResolvedValue({ sessionId: 'sess-1', optionsJson: '{"challenge":"c"}' })
    mockStartAuthentication.mockResolvedValue('{"id":"cred-1"}')
    mockFinishPasskeyElevation.mockResolvedValue({ elevationExpiresAt: until })

    await expect(elevateWithPasskey()).resolves.toBe(until)
    expect(mockStartAuthentication).toHaveBeenCalledWith('{"challenge":"c"}')
    expect(mockFinishPasskeyElevation).toHaveBeenCalledWith({
      sessionId: 'sess-1',
      credentialJson: '{"id":"cred-1"}',
    })
  })

  it('dropElevation calls the hub', async () => {
    mockDropElevation.mockResolvedValue({})
    await dropElevation()
    expect(mockDropElevation).toHaveBeenCalledWith({})
  })
})

describe('oauthReauthUrl', () => {
  it('escapes both the provider and the return address', () => {
    expect(oauthReauthUrl('git hub', '/oauth/authorize?state=a b'))
      .toBe('/auth/idp/git%20hub/reauth?redirect=%2Foauth%2Fauthorize%3Fstate%3Da%20b')
  })
})
