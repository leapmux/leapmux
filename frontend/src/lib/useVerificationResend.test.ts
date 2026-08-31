import type { Timestamp } from '@bufbuild/protobuf/wkt'
import { timestampFromDate } from '@bufbuild/protobuf/wkt'
import { createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { CaptchaProvider } from '~/generated/proto/leapmux/v1/auth_pb'
import { resetSystemInfoMock, setSystemInfoMock } from '~/test-support/systemInfoMock'

import { useVerificationResend } from './useVerificationResend'

const mockResend = vi.fn()

vi.mock('~/api/clients', () => ({
  userClient: {
    resendVerificationEmail: (...args: unknown[]) => mockResend(...args),
  },
}))

// The resend leg carries a captcha form; the default mock answers with a
// loaded snapshot and captcha disabled, so blocksSubmit() is false.
vi.mock('~/lib/systemInfo', async () => {
  const m = await import('~/test-support/systemInfoMock')
  return m.systemInfoMock
})

describe('useVerificationResend', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetSystemInfoMock()
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-01-01T00:00:00Z'))
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('starts a cooldown after a successful send', async () => {
    mockResend.mockResolvedValue({
      emailSent: true,
      nextResendAvailableAt: timestampFromDate(new Date(Date.now() + 42_000)),
    })

    await createRoot(async (dispose) => {
      const hook = useVerificationResend()
      await hook.resend()
      expect(hook.status()).toContain('fresh code')
      expect(hook.buttonLabel()).toBe('Resend code (0:42)')
      expect(hook.disabled()).toBe(true)
      dispose()
    })
  })

  it('applies cooldown even when the email was not sent', async () => {
    mockResend.mockResolvedValue({
      emailSent: false,
      nextResendAvailableAt: timestampFromDate(new Date(Date.now() + 60_000)),
    })

    await createRoot(async (dispose) => {
      const hook = useVerificationResend()
      await hook.resend()
      expect(hook.status()).toContain('couldn\'t send')
      expect(hook.buttonLabel()).toBe('Resend code (1:00)')
      expect(hook.disabled()).toBe(true)
      dispose()
    })
  })

  it('seeds the cooldown from an accessor that lands after mount', async () => {
    // The FOOTGUNS-2 regression: a hard reload of /verify-email starts with
    // the auth signal unset and receives it once the session restores. A
    // one-time value read would miss it; the accessor must re-seed.
    let seed: Timestamp | undefined
    await createRoot(async (dispose) => {
      const hook = useVerificationResend({ nextResendAvailableAt: () => seed })
      expect(hook.buttonLabel()).toBe('Resend code')
      expect(hook.disabled()).toBe(false)

      seed = timestampFromDate(new Date(Date.now() + 42_000))
      await Promise.resolve()
      expect(hook.buttonLabel()).toBe('Resend code (0:42)')
      expect(hook.disabled()).toBe(true)
      dispose()
    })
  })

  it('keeps the resend disabled while a required captcha is unsolved', async () => {
    // Fail closed: with captcha enabled and no solved payload, the button
    // stays disabled and the click is a no-op, so a page cannot round-trip
    // into a uniform PermissionDenied the cooldown UI then has to explain.
    setSystemInfoMock({ captchaEnabled: true, captchaProvider: CaptchaProvider.ALTCHA })

    await createRoot(async (dispose) => {
      const hook = useVerificationResend()
      expect(hook.countdown()).toBe(0)
      expect(hook.disabled()).toBe(true)
      await hook.resend()
      expect(mockResend).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('counts down toward zero', async () => {
    mockResend.mockResolvedValue({
      emailSent: true,
      nextResendAvailableAt: timestampFromDate(new Date(Date.now() + 3_000)),
    })

    await createRoot(async (dispose) => {
      const hook = useVerificationResend()
      await hook.resend()
      expect(hook.buttonLabel()).toBe('Resend code (0:03)')
      vi.advanceTimersByTime(2000)
      expect(hook.buttonLabel()).toBe('Resend code (0:01)')
      vi.advanceTimersByTime(2000)
      expect(hook.buttonLabel()).toBe('Resend code')
      expect(hook.disabled()).toBe(false)
      dispose()
    })
  })
})
