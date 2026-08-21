/// <reference types="vitest/globals" />
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { CaptchaProvider } from '~/generated/leapmux/v1/auth_pb'
import { getAltchaAlgorithm, getCaptchaProvider, getCaptchaSiteKey, isCaptchaEnabled, isSignupEnabled, isSoloMode, isSystemInfoLoaded, loadSystemInfo, refreshSnapshot } from './systemInfo'

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
    captchaEnabled: false,
    captchaProvider: CaptchaProvider.ALTCHA,
    captchaSiteKey: '',
    altchaAlgorithm: '',
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

  // Last on purpose: a successful load flips the module's one-way `loaded`
  // latch, and earlier tests assert retry behavior that assumes it is off.
  it('caches the hub\'s captcha flags for the pre-login gating', async () => {
    mockGetSystemInfo.mockResolvedValue(systemInfoResponse({
      captchaEnabled: true,
      altchaAlgorithm: 'PBKDF2/SHA-256',
    }))
    await loadSystemInfo(true)
    expect(isCaptchaEnabled()).toBe(true)
    expect(getAltchaAlgorithm()).toBe('PBKDF2/SHA-256')
    expect(getCaptchaProvider()).toBe(CaptchaProvider.ALTCHA)
    expect(getCaptchaSiteKey()).toBe('')
    expect(isSystemInfoLoaded()).toBe(true)
  })

  // ALTCHA needs SubtleCrypto (secure context only). On plain HTTP away
  // from localhost the hub runtime-gates too; the frontend stands down
  // from isSecureContext so a stale snapshot cannot deadlock the form.
  // External providers keep working on HTTP.
  it('stands down ALTCHA on a non-secure context without touching external providers', async () => {
    const desc = Object.getOwnPropertyDescriptor(window, 'isSecureContext')
    Object.defineProperty(window, 'isSecureContext', { configurable: true, get: () => false })
    try {
      mockGetSystemInfo.mockResolvedValue(systemInfoResponse({
        captchaEnabled: true,
        captchaProvider: CaptchaProvider.ALTCHA,
      }))
      await loadSystemInfo(true)
      expect(isCaptchaEnabled()).toBe(false)

      mockGetSystemInfo.mockResolvedValue(systemInfoResponse({
        captchaEnabled: true,
        captchaProvider: CaptchaProvider.TURNSTILE,
        captchaSiteKey: '1x00AA',
      }))
      await loadSystemInfo(true)
      expect(isCaptchaEnabled()).toBe(true)

      mockGetSystemInfo.mockResolvedValue(systemInfoResponse({
        captchaEnabled: true,
        captchaProvider: CaptchaProvider.RECAPTCHA_V3,
        captchaSiteKey: '6LeTest',
      }))
      await loadSystemInfo(true)
      expect(isCaptchaEnabled()).toBe(true)
    }
    finally {
      if (desc)
        Object.defineProperty(window, 'isSecureContext', desc)
      else
        delete (window as { isSecureContext?: boolean }).isSecureContext
    }
  })

  // A force reload rewrites the captcha signals, which is what the
  // denial-driven refresh after an admin toggles captcha at runtime
  // depends on: a `<Show>` gate reading isCaptchaEnabled() re-evaluates
  // instead of staying frozen at the first answer.
  it('flips the captcha flags on a forced reload', async () => {
    // The previous test loaded the module; the flip below is what matters.
    expect(isSystemInfoLoaded()).toBe(true)

    mockGetSystemInfo.mockResolvedValue(systemInfoResponse({ captchaEnabled: false }))
    await loadSystemInfo(true)
    expect(isCaptchaEnabled()).toBe(false)

    mockGetSystemInfo.mockResolvedValue(systemInfoResponse({
      captchaEnabled: true,
      captchaProvider: CaptchaProvider.TURNSTILE,
      captchaSiteKey: '1x00AA',
    }))
    await loadSystemInfo(true)
    expect(isCaptchaEnabled()).toBe(true)
    // The provider and site key flip with the flag: the widget layer
    // mounts the external field instead of the ALTCHA one.
    expect(getCaptchaProvider()).toBe(CaptchaProvider.TURNSTILE)
    expect(getCaptchaSiteKey()).toBe('1x00AA')
  })

  // The one-snapshot design: EVERY getter is reactive, including the
  // pre-existing ones (soloMode, signupEnabled, ...). A consumer that
  // reads one inside a reactive scope re-evaluates when a forced reload
  // changes it — the LoginPage signup link no longer needs an
  // auth.loading() crutch to appear on a direct load.
  it('re-evaluates reactive reads of non-captcha fields on a forced reload', async () => {
    mockGetSystemInfo.mockResolvedValue(systemInfoResponse({ signupEnabled: false }))
    await loadSystemInfo(true)

    const { createEffect, createRoot } = await import('solid-js')
    const seen: boolean[] = []
    createRoot(() => {
      createEffect(() => {
        seen.push(isSignupEnabled())
      })
    })
    expect(seen).toEqual([false])

    mockGetSystemInfo.mockResolvedValue(systemInfoResponse({ signupEnabled: true }))
    await loadSystemInfo(true)
    // A forced reload must re-run effects that read the snapshot.
    expect(seen).toEqual([false, true])
  })
})

describe('refreshSnapshot', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockGetSystemInfo.mockResolvedValue(systemInfoResponse({}))
  })

  // The convergence primitive's dedupe: the failure sites that suspect a
  // stale snapshot (a captcha denial, an arm failure) can fire within
  // milliseconds of each other for one underlying event, and the window
  // collapses them into a single fetch. A genuinely new failure outside
  // the window still refreshes.
  it('collapses refreshes inside the dedupe window and refreshes again after it', async () => {
    vi.useFakeTimers()
    try {
      refreshSnapshot()
      refreshSnapshot()
      await vi.waitFor(() => {
        expect(mockGetSystemInfo).toHaveBeenCalledTimes(1)
      })

      // Inside the window: a third trigger still owes nothing.
      refreshSnapshot()
      await Promise.resolve()
      expect(mockGetSystemInfo).toHaveBeenCalledTimes(1)

      // Past the window: the next trigger fetches again.
      vi.advanceTimersByTime(4000)
      refreshSnapshot()
      await vi.waitFor(() => {
        expect(mockGetSystemInfo).toHaveBeenCalledTimes(2)
      })
    }
    finally {
      vi.useRealTimers()
    }
  })
})
