/// <reference types="vitest/globals" />
import { Code, ConnectError } from '@connectrpc/connect'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { mockLoadSystemInfo, resetSystemInfoMock, setSystemInfoMock } from '~/test-support/systemInfoMock'

import { createCaptchaForm } from './captchaForm'

vi.mock('~/lib/systemInfo', async () => {
  const m = await import('~/test-support/systemInfoMock')
  return m.systemInfoMock
})

describe('createCaptchaForm', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetSystemInfoMock()
  })

  it('requires the payload only once the hub answers and enables captcha', () => {
    setSystemInfoMock({ loaded: false, captchaEnabled: true })
    const captcha = createCaptchaForm()
    // Fail closed during bootstrap: an unknown policy must block submit
    // rather than send an empty payload the hub denies.
    expect(captcha.pending()).toBe(true)
    expect(captcha.blocksSubmit()).toBe(true)
    expect(captcha.required()).toBe(false)

    setSystemInfoMock({ loaded: true })
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
    setSystemInfoMock({ captchaEnabled: true })
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
    setSystemInfoMock({ captchaEnabled: true })
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

  it('reset refetches the system info after a captcha denial so a runtime provider switch converges after one denial', async () => {
    setSystemInfoMock({ captchaEnabled: true })
    const captcha = createCaptchaForm()

    captcha.reset(new ConnectError('captcha verification failed', Code.PermissionDenied))

    // The denial is the signal that the captcha snapshot is stale: the
    // deduped forced reload is what re-mounts the right provider's field.
    expect(mockLoadSystemInfo).toHaveBeenCalledWith(true)
    await Promise.resolve()
  })

  it('reset skips the system-info refetch for failures that cannot change the captcha policy', async () => {
    setSystemInfoMock({ captchaEnabled: true })
    const captcha = createCaptchaForm()

    // A wrong password and a transport fault say nothing about the
    // captcha policy, so neither may double the form's request load.
    captcha.reset(new ConnectError('invalid credentials', Code.Unauthenticated))
    captcha.reset(new ConnectError('connection refused', Code.Unavailable))
    captcha.reset(new Error('not even a connect error'))

    expect(mockLoadSystemInfo).not.toHaveBeenCalled()
    await Promise.resolve()
  })
})
