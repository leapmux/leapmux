/// <reference types="vitest/globals" />
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { createCaptchaForm } from './captchaForm'

const mockIsCaptchaEnabled = vi.fn<() => boolean>(() => false)
const mockIsSystemInfoLoaded = vi.fn<() => boolean>(() => true)
vi.mock('~/lib/systemInfo', () => ({
  isCaptchaEnabled: () => mockIsCaptchaEnabled(),
  isSystemInfoLoaded: () => mockIsSystemInfoLoaded(),
}))

describe('createCaptchaForm', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockIsCaptchaEnabled.mockReturnValue(false)
    mockIsSystemInfoLoaded.mockReturnValue(true)
  })

  it('requires the payload only once the hub answers and enables captcha', () => {
    mockIsSystemInfoLoaded.mockReturnValue(false)
    mockIsCaptchaEnabled.mockReturnValue(true)
    const captcha = createCaptchaForm()
    // Fail closed during bootstrap: an unknown policy must block submit
    // rather than send an empty payload the hub denies.
    expect(captcha.pending()).toBe(true)
    expect(captcha.blocksSubmit()).toBe(true)
    expect(captcha.required()).toBe(false)

    mockIsSystemInfoLoaded.mockReturnValue(true)
    expect(captcha.pending()).toBe(false)
    expect(captcha.required()).toBe(true)
    expect(captcha.blocksSubmit()).toBe(true)

    captcha.setPayload('solved')
    expect(captcha.blocksSubmit()).toBe(false)
    expect(captcha.fields()).toEqual({ captchaPayload: 'solved', honeypot: '' })
  })

  it('never blocks when the hub answers captcha-disabled', () => {
    const captcha = createCaptchaForm()
    expect(captcha.required()).toBe(false)
    expect(captcha.blocksSubmit()).toBe(false)
    expect(captcha.fields()).toEqual({ captchaPayload: '', honeypot: '' })
  })

  it('lifts the requirement when the hub answers no challenge, and a reset re-arms', () => {
    mockIsCaptchaEnabled.mockReturnValue(true)
    const captcha = createCaptchaForm()
    expect(captcha.blocksSubmit()).toBe(true)

    captcha.noteUnavailable()
    expect(captcha.required()).toBe(false)
    expect(captcha.blocksSubmit()).toBe(false)

    captcha.reset()
    // A reset re-arms against the (stale) snapshot.
    expect(captcha.required()).toBe(true)
  })

  it('reset clears the payload and the honeypot and drives the field handle', () => {
    mockIsCaptchaEnabled.mockReturnValue(true)
    const captcha = createCaptchaForm()
    const fieldReset = vi.fn()
    captcha.bindField({ reset: fieldReset })

    captcha.setPayload('consumed')
    captcha.setHoneypot('http://spam.example')
    captcha.reset()

    expect(captcha.payload()).toBeNull()
    // A honeypot an autofill heuristic filled must not poison the retry.
    expect(captcha.honeypot()).toBe('')
    expect(captcha.fields()).toEqual({ captchaPayload: '', honeypot: '' })
    expect(fieldReset).toHaveBeenCalledOnce()
  })
})
