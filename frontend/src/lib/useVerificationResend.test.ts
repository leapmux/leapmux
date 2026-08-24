import type { Timestamp } from '@bufbuild/protobuf/wkt'
import { timestampFromDate } from '@bufbuild/protobuf/wkt'
import { createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { useVerificationResend } from './useVerificationResend'

const mockResend = vi.fn()

vi.mock('~/api/clients', () => ({
  userClient: {
    resendVerificationEmail: (...args: unknown[]) => mockResend(...args),
  },
}))

describe('useVerificationResend', () => {
  beforeEach(() => {
    vi.clearAllMocks()
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
