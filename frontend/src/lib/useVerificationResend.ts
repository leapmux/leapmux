import type { Timestamp } from '@bufbuild/protobuf/wkt'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { createEffect, createMemo, createSignal, onCleanup } from 'solid-js'
import { userClient } from '~/api/clients'
import { formatErrorMessage } from '~/lib/errors'

function cooldownSeconds(untilMs: number, nowMs: number): number {
  return Math.max(0, Math.ceil((untilMs - nowMs) / 1000))
}

export interface UseVerificationResendOptions {
  /**
   * Accessor for the cooldown seed from a login/signup response. An
   * accessor (not a value) so a session that restores after this hook
   * mounts still seeds the countdown — a hard reload of /verify-email
   * starts with the signal unset and receives it once auth bootstraps.
   */
  nextResendAvailableAt?: () => Timestamp | undefined
}

export function useVerificationResend(options?: UseVerificationResendOptions) {
  const seededUntil = (): number | null => {
    const t = options?.nextResendAvailableAt?.()
    if (!t)
      return null
    const ms = timestampDate(t).getTime()
    return ms > Date.now() ? ms : null
  }

  const [resending, setResending] = createSignal(false)
  const [status, setStatus] = createSignal<string | null>(null)
  const [error, setError] = createSignal<string | null>(null)
  const [cooldownUntil, setCooldownUntil] = createSignal<number | null>(seededUntil())
  const [now, setNow] = createSignal(Date.now())

  // The 1 Hz tick runs only while a countdown is actually pending; an idle
  // verify-email tab keeps no timer and no signal writes.
  let tick: ReturnType<typeof setInterval> | undefined
  const stopTick = () => {
    if (tick !== undefined) {
      clearInterval(tick)
      tick = undefined
    }
  }
  createEffect(() => {
    const until = cooldownUntil()
    if (until !== null && until > now()) {
      if (tick === undefined)
        tick = setInterval(() => setNow(Date.now()), 1000)
    }
    else {
      stopTick()
    }
  })
  onCleanup(stopTick)

  // Follow the auth signal: a session restored after mount can still set
  // the cooldown (only when it is later than any local cooldown).
  createEffect(() => {
    const seeded = seededUntil()
    if (seeded !== null && seeded > (cooldownUntil() ?? 0))
      setCooldownUntil(seeded)
  })

  const countdown = createMemo(() => {
    const until = cooldownUntil()
    if (until === null)
      return 0
    return cooldownSeconds(until, now())
  })

  const buttonLabel = createMemo(() => {
    if (resending())
      return 'Sending…'
    const secs = countdown()
    if (secs > 0) {
      const mm = Math.floor(secs / 60)
      const ss = secs % 60
      return `Resend code (${mm}:${String(ss).padStart(2, '0')})`
    }
    return 'Resend code'
  })

  const disabled = () => resending() || countdown() > 0

  const resend = async () => {
    if (disabled())
      return
    setResending(true)
    setStatus(null)
    setError(null)
    try {
      const resp = await userClient.resendVerificationEmail({})
      setStatus(
        resp.emailSent
          ? 'A fresh code has been sent to your inbox.'
          : 'We couldn\'t send the email — please try again shortly.',
      )
      if (resp.nextResendAvailableAt) {
        setCooldownUntil(timestampDate(resp.nextResendAvailableAt).getTime())
      }
    }
    catch (e) {
      setError(formatErrorMessage(e, 'Failed to resend verification email'))
    }
    finally {
      setResending(false)
    }
  }

  return {
    resend,
    resending,
    status,
    error,
    setError,
    setStatus,
    buttonLabel,
    disabled,
    countdown,
  }
}
