import type { Component } from 'solid-js'
import { A, useNavigate, useSearchParams } from '@solidjs/router'
import { createSignal, Show } from 'solid-js'
import { authClient } from '~/api/clients'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { AuthMethodPillGroup } from '~/components/common/AuthMethodPillGroup'
import { CaptchaSection } from '~/components/common/CaptchaSection'
import { Spinner } from '~/components/common/Spinner'
import { createAuthMethodSelection } from '~/lib/authMethodSelection'
import { createCaptchaForm } from '~/lib/captchaForm'
import { stringParam } from '~/lib/searchParam'
import { passkeyErrorMessage, startRegistration } from '~/lib/webauthn'
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
  const methodSelection = createAuthMethodSelection('recovery')
  const effectiveMethod = methodSelection.effectiveMethod

  const token = () => stringParam(searchParams.token) ?? ''

  const pwProps = { password, confirmPassword }

  // The link can be spent on either factor. Whichever the user picks, the
  // completion revokes every existing passkey and signs every session out;
  // the passkey path ALSO clears the password. Linked providers stay.
  const handleSubmit = async (e: Event) => {
    e.preventDefault()
    const recoveryToken = token()
    if (!recoveryToken) {
      setError('Missing recovery token. Open the link from your email.')
      return
    }
    if (effectiveMethod() === 'password' && !passwordCanSubmit(pwProps))
      return
    setSubmitting(true)
    setError(null)
    try {
      if (effectiveMethod() === 'passkey') {
        const begin = await authClient.beginAccountRecoveryPasskey({
          token: recoveryToken,
          ...captcha.fields(),
        })
        const credentialJson = await startRegistration(begin.optionsJson)
        await authClient.finishAccountRecoveryPasskey({
          token: recoveryToken,
          sessionId: begin.sessionId,
          credentialJson,
        })
      }
      else {
        await authClient.completeAccountRecoveryPassword({
          token: recoveryToken,
          newPassword: password(),
          ...captcha.fields(),
        })
      }
      navigate('/login', { replace: true })
    }
    catch (err) {
      // A dismissed passkey prompt is not a recovery failure: leave the
      // banner empty and let the user try the link again. Begin already
      // charged one attempt of the shared budget, so a retry still draws
      // down that cap.
      setError(passkeyErrorMessage(err, 'Account recovery failed') ?? '')
      captcha.reset(err)
      setSubmitting(false)
    }
  }

  const canSubmit = () =>
    captcha.blocksSubmit()
      ? false
      : effectiveMethod() === 'passkey' ? true : passwordCanSubmit(pwProps)

  return (
    <div class={styles.container}>
      <div class={pageCard}>
        <h1>Recover your account</h1>
        <p>This link recovers the account it was sent to: every session and connected app is signed out, and any existing passkeys are removed. Recovering with a passkey also removes the password.</p>
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
            <AuthMethodPillGroup
              label="Recovery method"
              selection={methodSelection}
            />
            <Show when={effectiveMethod() === 'password'}>
              <PasswordFields
                password={password}
                setPassword={setPassword}
                confirmPassword={confirmPassword}
                setConfirmPassword={setConfirmPassword}
              />
            </Show>
            <CaptchaSection action={methodSelection.captchaAction()} captcha={captcha} />
            <Show when={error()}>
              <div class={errorText}>{error()}</div>
            </Show>
            <div class={actionsFooter}>
              <button type="submit" disabled={submitting() || !canSubmit()}>
                <Show when={submitting()}><Spinner /></Show>
                {submitting()
                  ? 'Recovering…'
                  : effectiveMethod() === 'passkey' ? 'Recover with passkey' : 'Set new password'}
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
