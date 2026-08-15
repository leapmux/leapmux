import type { CaptchaFieldHandle } from './CaptchaField'
/// <reference types="vitest/globals" />
import { render } from '@solidjs/testing-library'

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { RecaptchaV3Field } from './RecaptchaV3Field'

const mockLoadScript = vi.fn(() => Promise.resolve())
vi.mock('~/lib/scriptLoader', () => ({
  loadExternalScript: (...args: []) => mockLoadScript(...args),
}))

vi.mock('~/lib/systemInfo', () => ({
  getCaptchaSiteKey: () => 'site-key',
}))

function installFakeGrecaptcha(execute = vi.fn(() => Promise.resolve('token'))) {
  window.grecaptcha = {
    ready: (cb: () => void) => cb(),
    execute,
  }
  return execute
}

function renderField(props: Partial<Parameters<typeof RecaptchaV3Field>[0]> = {}) {
  const onPayload = props.onPayload ?? vi.fn()
  return render(() => (
    <div><RecaptchaV3Field action={props.action ?? 'login'} onPayload={onPayload} {...props} /></div>
  ))
}

describe('recaptchaV3Field', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.clearAllMocks()
    delete window.grecaptcha
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('executes for the form action under the hub site key and delivers the token', async () => {
    const execute = installFakeGrecaptcha()
    const onPayload = vi.fn()
    renderField({ action: 'complete_signup', onPayload })
    await vi.waitFor(() => {
      expect(onPayload).toHaveBeenCalledWith('token')
    })
    expect(execute).toHaveBeenCalledWith('site-key', { action: 'complete_signup' })
    expect(mockLoadScript).toHaveBeenCalledWith(`https://www.google.com/recaptcha/api.js?render=${encodeURIComponent('site-key')}`)
  })

  it('re-executes inside the two-minute token window', async () => {
    const execute = installFakeGrecaptcha()
    const onPayload = vi.fn()
    renderField({ onPayload })
    await vi.waitFor(() => {
      expect(execute).toHaveBeenCalledTimes(1)
    })

    vi.advanceTimersByTime(110_000)
    await vi.waitFor(() => {
      expect(execute).toHaveBeenCalledTimes(2)
    })
  })

  it('skips the refresh while the tab is hidden and re-arms on visibility', async () => {
    const execute = installFakeGrecaptcha()
    const onPayload = vi.fn()
    renderField({ onPayload })
    await vi.waitFor(() => {
      expect(execute).toHaveBeenCalledTimes(1)
    })

    // A hidden tab cannot submit; the interval must not mint tokens
    // nobody uses.
    Object.defineProperty(document, 'hidden', { configurable: true, get: () => true })
    try {
      vi.advanceTimersByTime(110_000)
      vi.advanceTimersByTime(110_000)
      expect(execute).toHaveBeenCalledTimes(1)

      // Returning to the tab re-arms immediately: the next submit needs a
      // token inside the two-minute window.
      Object.defineProperty(document, 'hidden', { configurable: true, get: () => false })
      document.dispatchEvent(new Event('visibilitychange'))
      await vi.waitFor(() => {
        expect(execute).toHaveBeenCalledTimes(2)
      })
    }
    finally {
      Object.defineProperty(document, 'hidden', { configurable: true, get: () => false })
    }
  })

  it('reset handle re-executes for a fresh token', async () => {
    const execute = installFakeGrecaptcha()
    let handle: CaptchaFieldHandle | undefined
    render(() => (
      <div><RecaptchaV3Field action="login" onPayload={vi.fn()} ref={h => (handle = h)} /></div>
    ))
    await vi.waitFor(() => {
      expect(execute).toHaveBeenCalledTimes(1)
    })

    handle!.reset()
    await vi.waitFor(() => {
      expect(execute).toHaveBeenCalledTimes(2)
    })
  })

  it('shows a load error and clears the payload when execution fails', async () => {
    installFakeGrecaptcha(vi.fn(() => Promise.reject(new Error('network'))))
    const onPayload = vi.fn()
    const { findByText } = renderField({ onPayload })
    expect(await findByText(/could not load the human-verification challenge/i)).toBeTruthy()
    expect(onPayload).toHaveBeenLastCalledWith(null)
  })

  it('a stale overlapping execute loses: the newer token wins', async () => {
    let call = 0
    const execute = vi.fn(() => {
      call++
      return call === 1 ? new Promise<string>(() => {}) : Promise.resolve('fresh')
    })
    installFakeGrecaptcha(execute)
    let handle: CaptchaFieldHandle | undefined
    const onPayload = vi.fn()
    render(() => (
      <div><RecaptchaV3Field action="login" onPayload={onPayload} ref={h => (handle = h)} /></div>
    ))
    await vi.waitFor(() => {
      expect(execute).toHaveBeenCalledTimes(1)
    })

    handle!.reset()
    await vi.waitFor(() => {
      expect(onPayload).toHaveBeenCalledWith('fresh')
    })

    // The hung first execute resolves nothing; only "fresh" was delivered.
    expect(onPayload).toHaveBeenCalledTimes(1)
  })

  it('stops refreshing and drops the payload on cleanup', async () => {
    const execute = installFakeGrecaptcha()
    const onPayload = vi.fn()
    const { unmount } = renderField({ onPayload })
    await vi.waitFor(() => {
      expect(execute).toHaveBeenCalledTimes(1)
    })

    unmount()
    expect(onPayload).toHaveBeenLastCalledWith(null)
    const callsAfterUnmount = execute.mock.calls.length
    vi.advanceTimersByTime(110_000)
    expect(execute.mock.calls.length).toBe(callsAfterUnmount)
  })
})
