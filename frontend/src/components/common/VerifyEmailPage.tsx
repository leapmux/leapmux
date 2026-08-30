import type { Component } from 'solid-js'
import { useNavigate, useSearchParams } from '@solidjs/router'
import { createEffect, createSignal, Show } from 'solid-js'
import { userClient } from '~/api/clients'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { CaptchaSection } from '~/components/common/CaptchaSection'
import { useAuth } from '~/context/AuthContext'
import { createCaptchaForm } from '~/lib/captchaForm'
import { formatErrorMessage } from '~/lib/errors'
import { stringParam } from '~/lib/searchParam'
import { useVerificationResend } from '~/lib/useVerificationResend'
import { errorText, pageCard } from '~/styles/shared.css'
import * as styles from './LoginPage.css'

// normalizeCode strips the formatting characters (whitespace, hyphens) and
// uppercases the result. It does NOT validate the alphabet — the
// backend's `verifycode.Normalize` is the source of truth for charset
// rules and rejects bad input with InvalidArgument. Forking the charset
// list across the frontend/backend boundary would only create two places to
// update.
function normalizeCode(input: string): string {
  return input.replace(/[\s-]/g, '').toUpperCase()
}

export const VerifyEmailPage: Component = () => {
  const auth = useAuth()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const [code, setCode] = createSignal('')
  const [submitting, setSubmitting] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)
  // The verify leg charges the per-code wrong-guess budget, which a script
  // burns for free, so it is captcha-protected like the sign-in it guards.
  const captcha = createCaptchaForm()
  const resend = useVerificationResend({
    nextResendAvailableAt: () => auth.verificationResendAvailableAt(),
  })

  const [decided, setDecided] = createSignal(false)
  createEffect(() => {
    // Wait for the auth bootstrap before deciding "not signed in": on a
    // fresh load the session cookie is often still restoring, and reading
    // auth.user() too early bounces a valid session to the login form
    // (LoginPage guards the same race with auth.loading()).
    if (auth.loading() || decided())
      return
    setDecided(true)
    // Pull the URL code (used for both prefill and auto-submit). It may
    // arrive in display form ("XXX-XXX") or raw ("XXXXXX").
    const urlCode = stringParam(searchParams.code) ?? ''

    if (!auth.user()) {
      // Not signed in. Send the user through login first, preserving the
      // code so the page can resume on the round-trip back. Note: login
      // already honors `?redirect=` (see LoginPage.tsx) — do NOT use a
      // different param name here.
      const next = urlCode ? `/verify-email?code=${encodeURIComponent(urlCode)}` : '/verify-email'
      navigate(`/login?redirect=${encodeURIComponent(next)}`, { replace: true })
      return
    }

    if (urlCode) {
      setCode(urlCode)
      void submitCode(urlCode)
    }
  })

  async function submitCode(raw: string) {
    const normalized = normalizeCode(raw)
    if (!normalized) {
      setError('Enter the 6-character code from your email.')
      return
    }
    if (captcha.blocksSubmit()) {
      // The email deep-link auto-submit can fire before the widget finishes;
      // a refused RPC would burn a guess for nothing, so wait it out.
      setError('The captcha is still solving — try again in a moment.')
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      const resp = await userClient.verifyEmail({ verificationToken: normalized, ...captcha.fields() })
      // The RESPONSE is the authoritative account, so adopt it first. A second
      // round trip can fail, and `refreshUser` discards its own failure, so a
      // page that read the account only through the refresh navigates home with
      // `emailVerified` still false for an address the hub just verified.
      // Preferences then renders "unverified / Verify", and
      // RegisterWorkerDialog keeps its email control disabled.
      //
      // adoptSameIdentityUser, never setAuth: this is the SAME identity, so it
      // is a refresh and not a transition. setAuth clears the elevation
      // deadline -- which is right when a sign-in lands a new user, and wrong
      // here, where the hub touched no elevation column at all. Clearing it
      // made Preferences report a verified session as unverified and hid the
      // "End now" button while the window was still open, and sent a CLI
      // consent bounce back through /elevate for a factor the hub would not
      // have asked for.
      if (resp.user)
        auth.adoptSameIdentityUser(resp.user)
      // The refresh stays for the two signals the response does NOT carry: the
      // resend cooldown and the elevation deadline.
      await auth.refreshUser()
      navigate('/', { replace: true })
    }
    catch (e) {
      setError(formatErrorMessage(e, 'Verification failed'))
      captcha.reset(e)
    }
    finally {
      setSubmitting(false)
    }
  }

  function handleSubmit(e: Event) {
    e.preventDefault()
    void submitCode(code())
  }

  return (
    <div class={styles.container}>
      <div class={pageCard}>
        <h1>Verify your email</h1>
        <p>
          Enter the 6-character code we sent to your inbox, or click the
          link in that email.
        </p>
        <form class="vstack gap-4" onSubmit={handleSubmit}>
          <input
            data-testid="verify-email-code-input"
            type="text"
            inputmode="text"
            autocomplete="one-time-code"
            placeholder="XXX-XXX"
            value={code()}
            onInput={e => setCode(e.currentTarget.value)}
            maxlength={16}
            required
          />
          <CaptchaSection action="verify_email" captcha={captcha} />
          <div class={actionsFooter}>
            <button
              type="submit"
              data-testid="verify-email-submit"
              disabled={submitting() || captcha.blocksSubmit()}
            >
              {submitting() ? 'Verifying…' : 'Verify'}
            </button>
          </div>
        </form>
        <Show when={error()}>
          {msg => <div class={errorText}>{msg()}</div>}
        </Show>
        <Show when={resend.error()}>
          {msg => <div class={errorText}>{msg()}</div>}
        </Show>
        <Show when={resend.status()}>
          {msg => <div data-testid="verify-email-resend-status">{msg()}</div>}
        </Show>
        <CaptchaSection action="resend_verification" captcha={resend.captcha} />
        <div class={actionsFooter}>
          <button
            type="button"
            data-testid="verify-email-resend"
            onClick={() => {
              // Clear the code error first. resend() clears only its own
              // signals, so a stale "invalid or expired verification code"
              // would otherwise render beside "A fresh code has been sent",
              // which reads as though the new code was rejected too.
              setError(null)
              void resend.resend()
            }}
            disabled={resend.disabled()}
          >
            {resend.buttonLabel()}
          </button>
        </div>
      </div>
    </div>
  )
}
