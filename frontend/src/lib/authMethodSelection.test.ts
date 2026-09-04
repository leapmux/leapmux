import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createAuthMethodSelection } from './authMethodSelection'

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
  // system info, and an admin can clear the hub's public URL, so the
  // blocker can appear with a passkey already selected. Every
  // reader must then see 'password' -- LoginPage and SignupForm each read
  // the RAW signal at one or two sites, which hid the password field, hid
  // the "recovery" link, and skipped the password validation while
  // the submit path already fell back.
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

  it('derives the recovery captcha actions for both methods', () => {
    setSystemInfoMock({ passkeyBlocker: null })
    createRoot((dispose) => {
      const recovery = createAuthMethodSelection('recovery')
      expect(recovery.captchaAction()).toBe('account_recovery_password')

      recovery.select('passkey')
      expect(recovery.captchaAction()).toBe('account_recovery_passkey')

      // The fallback rule covers the recovery kind too: a hub that stops
      // serving this origin mid-form must not send the hub a passkey
      // token for a password submission.
      setSystemInfoMock({ passkeyBlocker: 'origin-not-allowed' })
      expect(recovery.captchaAction()).toBe('account_recovery_password')
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
