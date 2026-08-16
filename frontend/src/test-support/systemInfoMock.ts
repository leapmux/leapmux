/// <reference types="vitest/globals" />
import { createSignal } from 'solid-js'
import { vi } from 'vitest'

import { CaptchaProvider } from '~/generated/leapmux/v1/auth_pb'

/**
 * The one shared mock of `~/lib/systemInfo`. Every getter reads through a
 * real Solid signal so components track them exactly as they track the
 * production module's snapshot signal (a plain vi.fn freezes the first
 * answer inside a `<Show>`); `setSystemInfoMock` patches values
 * reactively, and `resetSystemInfoMock` restores the defaults between
 * tests.
 *
 * `loadSystemInfo` is the assertion seam: `refreshSnapshot` delegates to
 * `loadSystemInfo(true)`, mirroring production, so a test asserts every
 * forced refresh — whichever failure site caused it — on one mock.
 *
 * Wire it up per test file:
 *
 *   vi.mock('~/lib/systemInfo', async () => {
 *     const m = await import('~/test-support/systemInfoMock')
 *     return m.systemInfoMock
 *   })
 */

export interface SystemInfoMockState {
  /** Answers `isSystemInfoLoaded`; flip to false to test bootstrap gating. */
  loaded: boolean
  soloMode: boolean
  signupEnabled: boolean
  setupRequired: boolean
  emailEnabled: boolean
  captchaEnabled: boolean
  captchaProvider: CaptchaProvider
  captchaSiteKey: string
  altchaAlgorithm: string
  workerHubUrl: string
}

const defaultState: SystemInfoMockState = {
  loaded: true,
  soloMode: false,
  signupEnabled: false,
  setupRequired: false,
  emailEnabled: false,
  captchaEnabled: false,
  captchaProvider: CaptchaProvider.ALTCHA,
  captchaSiteKey: '',
  altchaAlgorithm: '',
  workerHubUrl: '',
}

const [state, setState] = createSignal<SystemInfoMockState>(defaultState)

export const mockLoadSystemInfo = vi.fn((_force?: boolean) => Promise.resolve())
// unknown[] so tests can supply plain provider fixtures without the
// generated message's $typeName marker.
export const mockLoadOAuthProviders = vi.fn((): Promise<unknown[]> => Promise.resolve([]))

export const systemInfoMock = {
  isSoloMode: () => state().soloMode,
  isSignupEnabled: () => state().signupEnabled,
  isSetupRequired: () => state().setupRequired,
  isEmailEnabled: () => state().emailEnabled,
  isSystemInfoLoaded: () => state().loaded,
  isCaptchaEnabled: () => state().captchaEnabled,
  getCaptchaProvider: () => state().captchaProvider,
  getCaptchaSiteKey: () => state().captchaSiteKey,
  getAltchaAlgorithm: () => state().altchaAlgorithm,
  getWorkerHubUrl: () => state().workerHubUrl,
  loadSystemInfo: mockLoadSystemInfo,
  refreshSnapshot: () => {
    void mockLoadSystemInfo(true)
  },
  loadOAuthProviders: mockLoadOAuthProviders,
}

/** Patch snapshot values reactively; every tracked read re-evaluates. */
export function setSystemInfoMock(patch: Partial<SystemInfoMockState>): void {
  setState({ ...state(), ...patch })
}

/** Restore the defaults and clear the call history, for beforeEach. */
export function resetSystemInfoMock(): void {
  setState(defaultState)
  mockLoadSystemInfo.mockClear()
  mockLoadOAuthProviders.mockReset().mockReturnValue(Promise.resolve([]))
}
