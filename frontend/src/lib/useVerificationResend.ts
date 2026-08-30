import type { Timestamp } from '@bufbuild/protobuf/wkt'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { createEffect, createMemo, createSignal, onCleanup } from 'solid-js'
import { userClient } from '~/api/clients'
import { createCaptchaForm } from '~/lib/captchaForm'
import { formatErrorMessage } from '~/lib/errors'

function cooldownSeconds(untilMs: number, nowMs: number): number {
  return Math.max(0, Math.ceil((untilMs - nowMs) / 1000))
}

export interface UseVerificationResendOptions {
  /**
   * Accessor for the cooldown seed. An accessor (not a value) so a session
   * that restores after this hook mounts still seeds the countdown: a hard
   * reload of /verify-email starts with the signal unset and receives it
   * once auth bootstraps.
   *
   * That re-seed needs `GetCurrentUserResponse.email_verification`, which
   * the bootstrap reads in `AuthContext.restoreSession`. Without it the
   * signal stayed undefined after a reload, the countdown restarted at
   * zero, and the click got a ResourceExhausted with no timestamp to
   * restart from.
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

  // The resend leg is captcha-protected like the verify leg: it drives an
  // SMTP send, and the cooldown gate alone does not pace a scripted
  // session. The form lives HERE rather than at each call site, so both
  // surfaces (/verify-email and Preferences → Account) send the same
  // fields and neither can forget the widget.
  const captcha = createCaptchaForm()

  const disabled = () => resending() || countdown() > 0 || captcha.blocksSubmit()

  const resend = async () => {
    if (disabled())
      return
    setResending(true)
    setStatus(null)
    setError(null)
    try {
      const resp = await userClient.resendVerificationEmail({ ...captcha.fields() })
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
      captcha.reset(e)
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
    captcha,
  }
}
