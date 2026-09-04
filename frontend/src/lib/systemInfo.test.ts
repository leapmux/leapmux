/// <reference types="vitest/globals" />
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { CaptchaProvider, SoloAccess } from '~/generated/proto/leapmux/v1/auth_pb'
import { captchaProviderNeedsSecureContext, getAltchaAlgorithm, getCaptchaProvider, getCaptchaSiteKey, isAutoAuthenticated, isCaptchaEnabled, isCaptchaUnsolvableHere, isPasswordSetupGate, isSignupEnabled, isSoloMode, isSystemInfoLoaded, loadSystemInfo, passkeyBlocker, passkeysUsableHere, passwordSetupRequired, refreshSnapshot, soloPasswordSet } from './systemInfo'

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
    soloAccess: SoloAccess.UNSPECIFIED,
    soloPasswordSet: false,
    signupEnabled: false,
    setupRequired: false,
    workerHubUrl: '',
    emailEnabled: false,
    passkeyEnabled: false,
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

  // A discarded failure leaves the module's defaults in place -- notably
  // `soloMode = false` -- with no signal that they are fabrications. On a solo
  // hub that means the app renders a "Log out" affordance whose one click sends
  // the user to a login form no credentials can satisfy. Callers must be able
  // to tell "the hub said non-solo" from "nobody got an answer".
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
  it('caches the hub\'s captcha flags for the pre-login forms', async () => {
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

  // ALTCHA needs SubtleCrypto (secure context only), so no widget can mount
  // on a plain-HTTP page away from localhost. The two answers stay SEPARATE:
  // isCaptchaEnabled reports what the hub requires, and
  // isCaptchaUnsolvableHere reports what this page can do about it. Folding
  // the second into the first made the getter's name disagree with its
  // value. External providers keep working on HTTP.
  it('reports an unsolvable ALTCHA page without changing what the hub requires', async () => {
    const desc = Object.getOwnPropertyDescriptor(window, 'isSecureContext')
    Object.defineProperty(window, 'isSecureContext', { configurable: true, get: () => false })
    try {
      mockGetSystemInfo.mockResolvedValue(systemInfoResponse({
        captchaEnabled: true,
        captchaProvider: CaptchaProvider.ALTCHA,
      }))
      await loadSystemInfo(true)
      expect(isCaptchaEnabled()).toBe(true)
      // The hub still REQUIRES it, and this page cannot solve it. The
      // forms block on that rather than submit an empty payload the hub
      // is certain to deny, which was a permanent denial loop.
      expect(isCaptchaUnsolvableHere()).toBe(true)

      mockGetSystemInfo.mockResolvedValue(systemInfoResponse({
        captchaEnabled: true,
        captchaProvider: CaptchaProvider.TURNSTILE,
        captchaSiteKey: '1x00AA',
      }))
      await loadSystemInfo(true)
      expect(isCaptchaEnabled()).toBe(true)
      expect(isCaptchaUnsolvableHere()).toBe(false)

      mockGetSystemInfo.mockResolvedValue(systemInfoResponse({
        captchaEnabled: true,
        captchaProvider: CaptchaProvider.RECAPTCHA_V3,
        captchaSiteKey: '6LeTest',
      }))
      await loadSystemInfo(true)
      expect(isCaptchaEnabled()).toBe(true)
      expect(isCaptchaUnsolvableHere()).toBe(false)

      // A hub that reports captcha OFF is not "unsolvable here": there is
      // nothing to solve, so the forms must not block or explain anything.
      mockGetSystemInfo.mockResolvedValue(systemInfoResponse({
        captchaEnabled: false,
        captchaProvider: CaptchaProvider.ALTCHA,
      }))
      await loadSystemInfo(true)
      expect(isCaptchaUnsolvableHere()).toBe(false)
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
  // auth.loading() workaround to appear on a direct load.
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

      // Inside the window: a third trigger still issues no fetch.
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

/**
 * Two parties decide whether a passkey ceremony can run, and each answers for
 * itself. The hub answers for the origin; the browser answers for the page.
 *
 * The bug this covers: a hub published at its own plain-HTTP address answers
 * passkey_enabled = true, every surface offered a passkey, and the ceremony
 * failed inside @simplewebauthn/browser with "WebAuthn is not supported in
 * this browser" -- a message that identifies the browser as the cause when the
 * browser is fine.
 */
describe('passkeyBlocker', () => {
  const credentialKey = 'PublicKeyCredential' as const

  /** Present or remove the WebAuthn API the way a browser does. */
  function setWebAuthnAPI(present: boolean): void {
    if (present)
      Object.defineProperty(globalThis, credentialKey, { configurable: true, value: class {} })
    else
      Reflect.deleteProperty(globalThis, credentialKey)
  }

  function setSecureContext(value: boolean | undefined): void {
    if (value === undefined)
      Reflect.deleteProperty(window, 'isSecureContext')
    else
      Object.defineProperty(window, 'isSecureContext', { configurable: true, get: () => value })
  }

  const originalCredential = Object.getOwnPropertyDescriptor(globalThis, credentialKey)
  const originalSecureContext = Object.getOwnPropertyDescriptor(window, 'isSecureContext')

  afterEach(() => {
    if (originalCredential)
      Object.defineProperty(globalThis, credentialKey, originalCredential)
    else
      Reflect.deleteProperty(globalThis, credentialKey)
    if (originalSecureContext)
      Object.defineProperty(window, 'isSecureContext', originalSecureContext)
    else
      Reflect.deleteProperty(window, 'isSecureContext')
  })

  it('reports no blocker when both parties agree', async () => {
    setWebAuthnAPI(true)
    setSecureContext(true)
    mockGetSystemInfo.mockResolvedValue(systemInfoResponse({ passkeyEnabled: true }))
    await loadSystemInfo(true)

    expect(passkeyBlocker()).toBeNull()
    expect(passkeysUsableHere()).toBe(true)
  })

  // The reported bug. The hub serves this origin, and the page is plain HTTP
  // away from loopback, so the browser exposes no WebAuthn API at all.
  it('identifies the insecure page when the browser exposes no WebAuthn API', async () => {
    setWebAuthnAPI(false)
    setSecureContext(false)
    mockGetSystemInfo.mockResolvedValue(systemInfoResponse({ passkeyEnabled: true }))
    await loadSystemInfo(true)

    expect(passkeyBlocker()).toBe('insecure-context')
    expect(passkeysUsableHere()).toBe(false)
  })

  // A secure page whose browser still has no WebAuthn is a different fact
  // with a different remedy, so it must not be reported as an insecure page.
  it('identifies the browser on a secure page that has no WebAuthn', async () => {
    setWebAuthnAPI(false)
    setSecureContext(true)
    mockGetSystemInfo.mockResolvedValue(systemInfoResponse({ passkeyEnabled: true }))
    await loadSystemInfo(true)

    expect(passkeyBlocker()).toBe('no-webauthn')
  })

  // jsdom leaves isSecureContext undefined, and an unknown context must not
  // be reported as an insecure one -- the same explicit-false rule the
  // captcha gate and the clipboard hold.
  it('does not call an unknown context insecure', async () => {
    setWebAuthnAPI(false)
    setSecureContext(undefined)
    mockGetSystemInfo.mockResolvedValue(systemInfoResponse({ passkeyEnabled: true }))
    await loadSystemInfo(true)

    expect(passkeyBlocker()).toBe('no-webauthn')
  })

  it('identifies the origin when the browser is ready and the hub refuses it', async () => {
    setWebAuthnAPI(true)
    setSecureContext(true)
    mockGetSystemInfo.mockResolvedValue(systemInfoResponse({ passkeyEnabled: false }))
    await loadSystemInfo(true)

    expect(passkeyBlocker()).toBe('origin-not-allowed')
  })

  // Both parties refuse. The BROWSER's reason takes precedence, because the
  // hub's reason would be wrong advice: an operator who published
  // http://hub.example:4327 and opened exactly that address reads advice to
  // open the configured URL, which is what they already did.
  it('reports the browser reason when both parties refuse', async () => {
    setWebAuthnAPI(false)
    setSecureContext(false)
    mockGetSystemInfo.mockResolvedValue(systemInfoResponse({ passkeyEnabled: false }))
    await loadSystemInfo(true)

    expect(passkeyBlocker()).toBe('insecure-context')
  })

  // The hub's half is a signal, so an admin who publishes the address
  // converges the affordance without a page reload -- which is what the
  // forced reload after a hub-settings write depends on.
  it('follows the hub across a forced reload', async () => {
    setWebAuthnAPI(true)
    setSecureContext(true)
    mockGetSystemInfo.mockResolvedValue(systemInfoResponse({ passkeyEnabled: false }))
    await loadSystemInfo(true)
    expect(passkeysUsableHere()).toBe(false)

    mockGetSystemInfo.mockResolvedValue(systemInfoResponse({ passkeyEnabled: true }))
    await loadSystemInfo(true)
    expect(passkeysUsableHere()).toBe(true)
  })
})

/**
 * The same rule the backend spells as `providerRequiresSecureContext`, in
 * `internal/hub/captcha/secure_context.go`.
 *
 * ALTCHA's proof of work calls SubtleCrypto, which a page holds only in a
 * secure context; Turnstile and reCAPTCHA v3 both run on a plain-HTTP page. A
 * fourth provider added without an entry here would answer `false` and put a
 * widget on a page that cannot mount it, so the rule is a named predicate on
 * both sides rather than a comparison spelled at one call site.
 */
describe('captchaProviderNeedsSecureContext', () => {
  it('claims ALTCHA, and nothing else', () => {
    expect(captchaProviderNeedsSecureContext(CaptchaProvider.ALTCHA)).toBe(true)
    expect(captchaProviderNeedsSecureContext(CaptchaProvider.TURNSTILE)).toBe(false)
    expect(captchaProviderNeedsSecureContext(CaptchaProvider.RECAPTCHA_V3)).toBe(false)
    expect(captchaProviderNeedsSecureContext(CaptchaProvider.UNSPECIFIED)).toBe(false)
  })
})

describe('the solo connection facts', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  // Every getter's PRE-LOAD answer is the safest guess, and for these three
  // that is false: an unloaded app then sends its visitor to the login form
  // rather than waiting for a session that never arrives, and never blocks
  // itself with a setup screen it has no answer for.
  it('reports the multi-user answer for a hub that says nothing', async () => {
    mockGetSystemInfo.mockResolvedValue(systemInfoResponse())
    await loadSystemInfo(true)

    expect(isAutoAuthenticated()).toBe(false)
    expect(passwordSetupRequired()).toBe(false)
    expect(soloPasswordSet()).toBe(false)
  })

  // solo_access is per connection and solo_mode is per hub, so the two
  // must not be read as synonyms: a solo hub whose account holds a password
  // asks its network callers to sign in.
  it('separates the hub fact from the connection fact', async () => {
    mockGetSystemInfo.mockResolvedValue(systemInfoResponse({
      soloMode: true,
      soloAccess: SoloAccess.SIGN_IN_REQUIRED,
      soloPasswordSet: true,
    }))
    await loadSystemInfo(true)

    expect(isSoloMode()).toBe(true)
    expect(isAutoAuthenticated()).toBe(false)
    expect(soloPasswordSet()).toBe(true)
  })

  it('carries the password-setup demand through', async () => {
    mockGetSystemInfo.mockResolvedValue(systemInfoResponse({
      soloMode: true,
      soloAccess: SoloAccess.PASSWORD_SETUP,
    }))
    await loadSystemInfo(true)

    expect(passwordSetupRequired()).toBe(true)
    expect(soloPasswordSet()).toBe(false)
  })

  it('derives each gate from one exclusive solo access state', async () => {
    mockGetSystemInfo.mockResolvedValue(systemInfoResponse({
      soloAccess: SoloAccess.PASSWORD_SETUP,
    }))
    await loadSystemInfo(true)
    expect(isPasswordSetupGate()).toBe(true)
    expect(isAutoAuthenticated()).toBe(false)

    mockGetSystemInfo.mockResolvedValue(systemInfoResponse({
      soloAccess: SoloAccess.CREDENTIAL_FREE,
    }))
    await loadSystemInfo(true)
    expect(isPasswordSetupGate()).toBe(false)
    expect(isAutoAuthenticated()).toBe(true)

    mockGetSystemInfo.mockResolvedValue(systemInfoResponse({
      soloAccess: SoloAccess.SIGN_IN_REQUIRED,
    }))
    await loadSystemInfo(true)
    expect(isPasswordSetupGate()).toBe(false)
  })
})
