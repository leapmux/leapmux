/// <reference types="vitest/globals" />
import type { PasskeyBlocker } from '~/lib/systemInfo'
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
  /** Answers `isSystemInfoLoaded`; flip to false to test the fail-closed bootstrap window. */
  loaded: boolean
  soloMode: boolean
  signupEnabled: boolean
  setupRequired: boolean
  emailEnabled: boolean
  /**
   * Answers `passkeyBlocker`: why this page cannot run a passkey ceremony,
   * or null when it can. Driven directly rather than derived from the hub
   * flag and `globalThis.PublicKeyCredential`, so a test states the
   * condition it means -- and so it can state a BROWSER condition (an
   * insecure page) that jsdom cannot produce.
   */
  passkeyBlocker: PasskeyBlocker | null
  captchaEnabled: boolean
  /**
   * Answers `isCaptchaUnsolvableHere`: the hub requires ALTCHA and this
   * page is not a secure context, so no widget can mount. Driven directly
   * rather than derived from `window.isSecureContext`, so a test states
   * the condition it means instead of the two facts that produce it.
   */
  captchaUnsolvableHere: boolean
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
  passkeyBlocker: null,
  captchaEnabled: false,
  captchaUnsolvableHere: false,
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
  passkeyBlocker: () => state().passkeyBlocker,
  passkeysUsableHere: () => state().passkeyBlocker === null,
  isSystemInfoLoaded: () => state().loaded,
  isCaptchaEnabled: () => state().captchaEnabled,
  isCaptchaUnsolvableHere: () => state().captchaUnsolvableHere,
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
