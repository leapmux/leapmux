import type { Component } from 'solid-js'
import { A } from '@solidjs/router'
import { createSignal, Show } from 'solid-js'
import { authClient } from '~/api/clients'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { CaptchaSection } from '~/components/common/CaptchaSection'
import { Spinner } from '~/components/common/Spinner'
import { createCaptchaForm } from '~/lib/captchaForm'
import { formatErrorMessage } from '~/lib/errors'
import { errorText, pageCard, successText } from '~/styles/shared.css'
import * as styles from './LoginPage.css'

export const ForgotPasswordPage: Component = () => {
  const [identifier, setIdentifier] = createSignal('')
  const [submitting, setSubmitting] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)
  const [submitted, setSubmitted] = createSignal(false)
  const captcha = createCaptchaForm()

  const handleSubmit = async (e: Event) => {
    e.preventDefault()
    if (!identifier().trim())
      return
    setSubmitting(true)
    setError(null)
    try {
      await authClient.requestPasswordReset({
        identifier: identifier().trim(),
        ...captcha.fields(),
      })
      setSubmitted(true)
    }
    catch (err) {
      setError(formatErrorMessage(err, 'Request failed'))
      captcha.reset(err)
      setSubmitting(false)
    }
  }

  return (
    <div class={styles.container}>
      <div class={pageCard}>
        <h1>Reset password</h1>
        <Show
          when={!submitted()}
          fallback={(
            <div class="vstack gap-4">
              <p class={successText}>
                If an account with that email or username exists, we sent a reset link.
              </p>
              <A href="/login">Back to login</A>
            </div>
          )}
        >
          <p>Enter your email or username and we will send a reset link if an account exists.</p>
          <form class="vstack gap-4" onSubmit={handleSubmit}>
            <label>
              Email or username
              <input
                type="text"
                value={identifier()}
                onInput={e => setIdentifier(e.currentTarget.value)}
                autocomplete="username"
                required
              />
            </label>
            <CaptchaSection action="password_reset" captcha={captcha} />
            <Show when={error()}>
              <div class={errorText}>{error()}</div>
            </Show>
            <div class={actionsFooter}>
              <button type="submit" disabled={submitting() || !identifier().trim() || captcha.blocksSubmit()}>
                <Show when={submitting()}><Spinner /></Show>
                {submitting() ? 'Sending…' : 'Send reset link'}
              </button>
            </div>
          </form>
          <div class={styles.authFooter}>
            <A href="/login">Back to login</A>
          </div>
        </Show>
      </div>
    </div>
  )
}
