/// <reference types="vitest/globals" />
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { isSoloMode, loadSystemInfo } from './systemInfo'

const mockGetSystemInfo = vi.fn()
vi.mock('~/api/clients', () => ({
  authClient: {
    getSystemInfo: (...args: unknown[]) => mockGetSystemInfo(...args),
    getOAuthProviders: vi.fn().mockResolvedValue({ providers: [] }),
  },
}))

vi.mock('~/api/platformBridge', () => ({
  desktopFetch: vi.fn(),
  getCapabilities: () => ({ hubTransport: 'direct' }),
  isTauriApp: () => false,
}))

function systemInfoResponse(overrides: Record<string, unknown> = {}) {
  return {
    soloMode: false,
    signupEnabled: false,
    setupRequired: false,
    workerHubUrl: '',
    emailEnabled: false,
    version: '',
    commitHash: '',
    commitTime: '',
    buildTime: '',
    branch: '',
    ...overrides,
  }
}

describe('loadSystemInfo', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  // A swallowed failure leaves the module's defaults in place -- notably
  // `soloMode = false` -- with no signal that they are fabrications. On a solo
  // hub that means the app renders a "Log out" affordance whose one click sends
  // the user to a login form no credentials can satisfy. Callers must be able
  // to tell "the hub said non-solo" from "we never got an answer".
  it('propagates a load failure instead of silently defaulting to non-solo', async () => {
    mockGetSystemInfo.mockRejectedValue(new Error('not connected'))

    await expect(loadSystemInfo(true)).rejects.toThrow('not connected')
    expect(isSoloMode()).toBe(false)
  })

  // A failed load must not mark the module loaded: the next unforced call has
  // to retry rather than serve the fabricated defaults forever.
  it('does not cache a failed load', async () => {
    mockGetSystemInfo.mockRejectedValueOnce(new Error('not connected'))
    await expect(loadSystemInfo(true)).rejects.toThrow('not connected')

    mockGetSystemInfo.mockResolvedValueOnce(systemInfoResponse({ soloMode: true }))
    await loadSystemInfo()

    expect(mockGetSystemInfo).toHaveBeenCalledTimes(2)
    expect(isSoloMode()).toBe(true)
  })

  it('serves the cached answer on a subsequent unforced call', async () => {
    mockGetSystemInfo.mockResolvedValue(systemInfoResponse({ soloMode: true }))
    await loadSystemInfo(true)
    await loadSystemInfo()

    expect(mockGetSystemInfo).toHaveBeenCalledOnce()
    expect(isSoloMode()).toBe(true)
  })
})
