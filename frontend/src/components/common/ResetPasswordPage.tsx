import type { Component } from 'solid-js'
import { A, useNavigate, useSearchParams } from '@solidjs/router'
import { createSignal, Show } from 'solid-js'
import { authClient } from '~/api/clients'
import { CaptchaSection } from '~/components/common/CaptchaSection'
import { Spinner } from '~/components/common/Spinner'
import { createCaptchaForm } from '~/lib/captchaForm'
import { formatErrorMessage } from '~/lib/errors'
import { errorText, pageCard } from '~/styles/shared.css'
import * as styles from './LoginPage.css'
import { passwordCanSubmit, PasswordFields } from './PasswordFields'

export const ResetPasswordPage: Component = () => {
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const [password, setPassword] = createSignal('')
  const [confirmPassword, setConfirmPassword] = createSignal('')
  const [submitting, setSubmitting] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)
  const captcha = createCaptchaForm()

  const token = () => typeof searchParams.token === 'string' ? searchParams.token : ''

  const pwProps = { password, confirmPassword }

  const handleSubmit = async (e: Event) => {
    e.preventDefault()
    if (!passwordCanSubmit(pwProps))
      return
    const resetToken = token()
    if (!resetToken) {
      setError('Missing reset token. Open the link from your email.')
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      await authClient.completePasswordReset({
        token: resetToken,
        newPassword: password(),
        ...captcha.fields(),
      })
      navigate('/login', { replace: true })
    }
    catch (err) {
      setError(formatErrorMessage(err, 'Password reset failed'))
      captcha.reset(err)
      setSubmitting(false)
    }
  }

  return (
    <div class={styles.container}>
      <div class={pageCard}>
        <h1>Choose a new password</h1>
        <Show
          when={token()}
          fallback={(
            <div class="vstack gap-4">
              <div class={errorText}>Missing reset token. Open the link from your email.</div>
              <A href="/forgot-password">Request a new link</A>
            </div>
          )}
        >
          <form class="vstack gap-4" onSubmit={handleSubmit}>
            <PasswordFields
              password={password}
              setPassword={setPassword}
              confirmPassword={confirmPassword}
              setConfirmPassword={setConfirmPassword}
            />
            <CaptchaSection action="complete_password_reset" captcha={captcha} />
            <Show when={error()}>
              <div class={errorText}>{error()}</div>
            </Show>
            <button type="submit" disabled={submitting() || !passwordCanSubmit(pwProps) || captcha.blocksSubmit()}>
              <Show when={submitting()}><Spinner /></Show>
              {submitting() ? 'Resetting…' : 'Reset password'}
            </button>
          </form>
        </Show>
        <div class={styles.authFooter}>
          <A href="/login">Back to login</A>
        </div>
      </div>
    </div>
  )
}
