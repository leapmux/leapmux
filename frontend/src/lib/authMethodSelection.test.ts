import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { authMethodOptions, createAuthMethodSelection } from './authMethodSelection'

vi.mock('~/lib/systemInfo', async () => {
  const m = await import('~/test-support/systemInfoMock')
  return m.systemInfoMock
})

const { resetSystemInfoMock, setSystemInfoMock } = await import('~/test-support/systemInfoMock')

describe('createAuthMethodSelection', () => {
  beforeEach(() => {
    resetSystemInfoMock()
  })

  it('starts on password', () => {
    createRoot((dispose) => {
      const s = createAuthMethodSelection('login')
      expect(s.effectiveMethod()).toBe('password')
      dispose()
    })
  })

  it('selects passkey when the hub can run ceremonies', () => {
    setSystemInfoMock({ passkeyBlocker: null })
    createRoot((dispose) => {
      const s = createAuthMethodSelection('login')
      s.select('passkey')
      expect(s.effectiveMethod()).toBe('passkey')
      dispose()
    })
  })

  // The regression this helper exists for. A captcha refusal re-fetches
  // system info, and an admin can clear the hub's public URL, so
  // The blocker can appear with a passkey already selected. Every
  // reader must then see 'password' -- LoginPage and SignupForm each read
  // the RAW signal at one or two sites, which hid the password field, hid
  // the "Forgot password?" link, and skipped the password validation while
  // the submit path had already fallen back.
  it('falls back to password when passkey support disappears mid-form', () => {
    setSystemInfoMock({ passkeyBlocker: null })
    createRoot((dispose) => {
      const s = createAuthMethodSelection('login')
      s.select('passkey')
      expect(s.effectiveMethod()).toBe('passkey')

      setSystemInfoMock({ passkeyBlocker: 'origin-not-allowed' })
      expect(s.effectiveMethod()).toBe('password')
      dispose()
    })
  })

  it('derives the captcha action from the effective method, not the raw one', () => {
    setSystemInfoMock({ passkeyBlocker: null })
    createRoot((dispose) => {
      const login = createAuthMethodSelection('login')
      const signup = createAuthMethodSelection('signup')
      expect(login.captchaAction()).toBe('login')
      expect(signup.captchaAction()).toBe('signup')

      login.select('passkey')
      signup.select('passkey')
      expect(login.captchaAction()).toBe('passkey_login')
      expect(signup.captchaAction()).toBe('passkey_signup')

      // The hub refuses a token minted under a different action, so the
      // fallback must move the action too.
      setSystemInfoMock({ passkeyBlocker: 'origin-not-allowed' })
      expect(login.captchaAction()).toBe('login')
      expect(signup.captchaAction()).toBe('signup')
      dispose()
    })
  })

  it('keeps two selections independent', () => {
    setSystemInfoMock({ passkeyBlocker: null })
    createRoot((dispose) => {
      const a = createAuthMethodSelection('login')
      const b = createAuthMethodSelection('signup')
      a.select('passkey')
      expect(a.effectiveMethod()).toBe('passkey')
      expect(b.effectiveMethod()).toBe('password')
      dispose()
    })
  })
})

/**
 * The pills BOTH forms render, stated once.
 *
 * Two shapes of refusal, and they are not the same to a reader: one is a
 * property of where they are standing and they can move, the other is a
 * property of the deployment and identical for every visitor. An answer that
 * differed between the login form and the sign-up form would be a bug neither
 * file could show.
 */
describe('authMethodOptions', () => {
  beforeEach(() => {
    resetSystemInfoMock()
  })

  it('offers both methods when a ceremony can run', () => {
    setSystemInfoMock({ passkeyBlocker: null })
    expect(authMethodOptions()).toEqual([
      { value: 'password', label: 'Password' },
      { value: 'passkey', label: 'Passkey', disabledReason: undefined },
    ])
  })

  // The BROWSER refuses: the pill stays and carries the reason, because
  // hiding it leaves somebody whose only credential is a passkey at a dead
  // end with nothing to read.
  it.each([
    ['insecure-context' as const, /secure page/i],
    ['no-webauthn' as const, /does not support passkeys/i],
  ])('keeps a refused passkey pill and says why for %s', (blocker, expected) => {
    setSystemInfoMock({ passkeyBlocker: blocker })
    const options = authMethodOptions()
    expect(options.map(o => o.value)).toEqual(['password', 'passkey'])
    expect(options[1]!.disabledReason).toMatch(expected)
  })

  // The HUB refuses: the pill goes. It is a property of the deployment,
  // identical for every visitor, and a permanently dead pill is noise.
  it('drops the passkey pill when the hub does not serve this origin', () => {
    setSystemInfoMock({ passkeyBlocker: 'origin-not-allowed' })
    expect(authMethodOptions().map(o => o.value)).toEqual(['password'])
  })

  // Whatever the pills show, the password arm survives -- it is the only way
  // in once the other one is refused.
  it.each([
    ['insecure-context' as const],
    ['no-webauthn' as const],
    ['origin-not-allowed' as const],
  ])('always leaves the password arm live under %s', (blocker) => {
    setSystemInfoMock({ passkeyBlocker: blocker })
    const password = authMethodOptions().find(o => o.value === 'password')
    expect(password).toBeDefined()
    expect(password!.disabledReason).toBeUndefined()
  })
})
