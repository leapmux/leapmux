import type { Component } from 'solid-js'
import { useNavigate, useSearchParams } from '@solidjs/router'
import { createSignal, onMount, Show } from 'solid-js'
import { userClient } from '~/api/clients'
import { useAuth } from '~/context/AuthContext'
import { cardNarrow, errorText } from '~/styles/shared.css'
import * as styles from './LoginPage.css'

// normalizeCode strips formatting noise (whitespace, hyphens) and
// uppercases the result. It does NOT validate the alphabet — the
// backend's `verifycode.Normalize` is the source of truth for charset
// rules and rejects bad input with InvalidArgument. Forking the charset
// list across the FE/BE boundary would just create two places to update.
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
  const [resending, setResending] = createSignal(false)
  const [resendStatus, setResendStatus] = createSignal<string | null>(null)

  onMount(async () => {
    // Pull the URL code (used for both prefill and auto-submit). It may
    // arrive in display form ("XXX-XXX") or raw ("XXXXXX").
    const urlCode = typeof searchParams.code === 'string' ? searchParams.code : ''

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
      await submitCode(urlCode)
    }
  })

  async function submitCode(raw: string) {
    const normalized = normalizeCode(raw)
    if (!normalized) {
      setError('Enter the 6-character code from your email.')
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      const resp = await userClient.verifyEmail({ verificationToken: normalized })
      // Refresh auth so EmailVerified flips in the cached user.
      if (resp.user)
        auth.setAuth(resp.user)
      const slug = resp.user?.username ?? auth.user()?.username
      navigate(slug ? `/o/${slug}` : '/', { replace: true })
    }
    catch (e) {
      setError(`${e}`)
    }
    finally {
      setSubmitting(false)
    }
  }

  function handleSubmit(e: Event) {
    e.preventDefault()
    void submitCode(code())
  }

  async function handleResend() {
    setResending(true)
    setResendStatus(null)
    setError(null)
    try {
      const resp = await userClient.resendVerificationEmail({})
      // The backend may have updated the row but skipped the SMTP send
      // (e.g. mail provider 5xx). Surface the difference so the user
      // doesn't sit waiting for an email that's not coming.
      setResendStatus(resp.emailSent ? 'A fresh code has been sent to your inbox.' : 'We couldn\'t send the email — please try again shortly.')
    }
    catch (e) {
      setError(`${e}`)
    }
    finally {
      setResending(false)
    }
  }

  return (
    <div class={styles.container}>
      <div class={`card ${cardNarrow}`}>
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
          <button
            type="submit"
            data-testid="verify-email-submit"
            disabled={submitting()}
          >
            {submitting() ? 'Verifying…' : 'Verify'}
          </button>
        </form>
        <Show when={error()}>
          {msg => <div class={errorText}>{msg()}</div>}
        </Show>
        <Show when={resendStatus()}>
          {msg => <div data-testid="verify-email-resend-status">{msg()}</div>}
        </Show>
        <button
          type="button"
          data-testid="verify-email-resend"
          onClick={() => void handleResend()}
          disabled={resending()}
        >
          {resending() ? 'Sending…' : 'Resend code'}
        </button>
      </div>
    </div>
  )
}
