import type { Component } from 'solid-js'
import { A, useNavigate, useSearchParams } from '@solidjs/router'
import { createSignal, Show } from 'solid-js'
import { authClient } from '~/api/clients'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { CaptchaSection } from '~/components/common/CaptchaSection'
import { Spinner } from '~/components/common/Spinner'
import { CAPTCHA_ACTION } from '~/generated/contracts/captcha'
import { createCaptchaForm } from '~/lib/captchaForm'
import { formatErrorMessage } from '~/lib/errors'
import { stringParam } from '~/lib/searchParam'
import { errorText, pageCard } from '~/styles/shared.css'
import * as styles from './LoginPage.css'
import { passwordCanSubmit, PasswordFields } from './PasswordFields'

export const RecoverCompletePage: Component = () => {
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const [password, setPassword] = createSignal('')
  const [confirmPassword, setConfirmPassword] = createSignal('')
  const [submitting, setSubmitting] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)
  const captcha = createCaptchaForm()

  const token = () => stringParam(searchParams.token) ?? ''

  const pwProps = { password, confirmPassword }

  const handleSubmit = async (e: Event) => {
    e.preventDefault()
    if (!passwordCanSubmit(pwProps))
      return
    const recoveryToken = token()
    if (!recoveryToken) {
      setError('Missing recovery token. Open the link from your email.')
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      await authClient.completeAccountRecovery({
        token: recoveryToken,
        newPassword: password(),
        ...captcha.fields(),
      })
      navigate('/login', { replace: true })
    }
    catch (err) {
      setError(formatErrorMessage(err, 'Account recovery failed'))
      captcha.reset(err)
      setSubmitting(false)
    }
  }

  return (
    <div class={styles.container}>
      <div class={pageCard}>
        <h1>Choose a new password</h1>
        <p>Setting a password recovers the account this link was sent to: every other session and connected app is signed out, and any passkeys are removed.</p>
        <Show
          when={token()}
          fallback={(
            <div class="vstack gap-4">
              <div class={errorText}>Missing recovery token. Open the link from your email.</div>
              <A href="/recover-account">Request a new link</A>
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
            <CaptchaSection action={CAPTCHA_ACTION.completeAccountRecovery} captcha={captcha} />
            <Show when={error()}>
              <div class={errorText}>{error()}</div>
            </Show>
            <div class={actionsFooter}>
              <button type="submit" disabled={submitting() || !passwordCanSubmit(pwProps) || captcha.blocksSubmit()}>
                <Show when={submitting()}><Spinner /></Show>
                {submitting() ? 'Setting…' : 'Set new password'}
              </button>
            </div>
          </form>
        </Show>
        <div class={styles.authFooter}>
          <A href="/login">Back to login</A>
        </div>
      </div>
    </div>
  )
}
