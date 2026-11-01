import type { Component, JSX } from 'solid-js'

import type { SessionReport } from '~/generated/proto/leapmux/v1/auth_pb'
import { createSignal, Show } from 'solid-js'
import { authClient } from '~/api/clients'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { AuthMethodPillGroup } from '~/components/common/AuthMethodPillGroup'
import { CaptchaSection } from '~/components/common/CaptchaSection'
import { createAuthMethodSelection } from '~/lib/authMethodSelection'
import { createCaptchaForm } from '~/lib/captchaForm'
import { isEmailEnabled } from '~/lib/systemInfo'
import { sanitizeDisplayName, sanitizeSlug, validateEmail, validateReservedUsername } from '~/lib/validate'
import { passkeyErrorMessage, registerForChallenge } from '~/lib/webauthn'
import { errorText } from '~/styles/shared.css'
import { passwordCanSubmit, PasswordFields } from './PasswordFields'
import { Spinner } from './Spinner'
import { UsernameField } from './UsernameField'

/**
 * What a successful sign-up hands its caller: the session it established.
 *
 * `report.user` is absent when the hub could not read the new account back.
 * The sign-up still succeeded, so the caller hands the report to
 * `auth.setAuth`, which starts sign-in recovery for it.
 */
export interface SignupResult {
  report: SessionReport | undefined
}

interface SignupFormProps {
  submitLabel: string
  submittingLabel: string
  errorPrefix?: string
  header?: JSX.Element
  /**
   * When true, the username field accepts `admin`. Used by the first-admin
   * setup flow. Defaults to false for public signup paths.
   */
  allowAdminUsername?: boolean
  /**
   * Runs once the account exists and the session is live.
   *
   * It may return a promise, and this form AWAITS it: every caller adopts the
   * session through `auth.setAuth`, which has to load the account's stored
   * preferences before it publishes the identity. A caller that navigated
   * without waiting would mount the next route on the previous account's
   * namespace.
   */
  onSuccess: (resp: SignupResult) => void | Promise<void>
}

export const SignupForm: Component<SignupFormProps> = (props) => {
  const [username, setUsername] = createSignal('')
  const [password, setPassword] = createSignal('')
  const [confirmPassword, setConfirmPassword] = createSignal('')
  const [displayName, setDisplayName] = createSignal('')
  const [displayNameEdited, setDisplayNameEdited] = createSignal(false)
  const [email, setEmail] = createSignal('')
  const methodSelection = createAuthMethodSelection('signup')
  const effectiveMethod = methodSelection.effectiveMethod
  const [submitting, setSubmitting] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)
  const captcha = createCaptchaForm()

  const pwProps = { password, confirmPassword }

  // The display name mirrors the username until the user edits it directly.
  // Once edited the mirror never re-arms -- not even after clearing the field
  // -- so later username typing cannot resurrect what the user deleted.
  const handleUsernameInput = (v: string) => {
    setUsername(v)
    if (!displayNameEdited()) {
      setDisplayName(v)
    }
  }

  const validateCommonFields = () => {
    const [slug, slugErr] = sanitizeSlug('Username', username())
    if (slugErr) {
      setError(slugErr)
      return null
    }
    const reservedErr = validateReservedUsername(slug, props.allowAdminUsername ?? false)
    if (reservedErr) {
      setError(reservedErr)
      return null
    }
    const { value: sanitizedDisplayName, error: dnErr } = sanitizeDisplayName(displayName(), slug)
    if (dnErr) {
      setError(dnErr)
      return null
    }
    const trimmedEmail = email().trim()
    // The hub requires an email only when SMTP is configured (it is the
    // verification channel); without SMTP the server accepts an empty
    // email, and the form must not be narrower than the contract.
    if (!trimmedEmail && isEmailEnabled()) {
      setError('Email is required.')
      return null
    }
    if (trimmedEmail) {
      const emailErr = validateEmail(trimmedEmail)
      if (emailErr) {
        setError(emailErr)
        return null
      }
    }
    return { slug, sanitizedDisplayName, email: trimmedEmail }
  }

  const handleSubmit = async (e: Event) => {
    e.preventDefault()
    const fields = validateCommonFields()
    if (!fields)
      return
    if (effectiveMethod() === 'password' && !passwordCanSubmit(pwProps))
      return
    setSubmitting(true)
    setError(null)
    try {
      let report: SessionReport | undefined
      if (effectiveMethod() === 'passkey') {
        const begin = await authClient.beginPasskeySignUp({
          username: fields.slug,
          displayName: fields.sanitizedDisplayName,
          email: fields.email,
          ...captcha.fields(),
        })
        const passkeyResp = await authClient.finishPasskeySignUp({
          credential: await registerForChallenge(begin.challenge),
        })
        report = passkeyResp.sessionReport
      }
      else {
        const passwordResp = await authClient.signUp({
          username: fields.slug,
          password: password(),
          displayName: fields.sanitizedDisplayName,
          email: fields.email,
          ...captcha.fields(),
        })
        report = passwordResp.sessionReport
      }
      // One success path for both branches. An absent account is not a
      // failure here: the sign-up succeeded, and the caller's setAuth starts
      // recovery for it.
      await props.onSuccess({ report })
    }
    catch (err) {
      // A dismissed passkey prompt is not a sign-up failure: leave the
      // banner empty and let the user try again.
      setError(passkeyErrorMessage(err, props.errorPrefix ?? 'Sign up failed') ?? '')
      captcha.reset(err)
      setSubmitting(false)
    }
  }

  const captchaAction = methodSelection.captchaAction

  const emailRequired = () => isEmailEnabled()

  const canSubmit = () => {
    if (!username() || (emailRequired() && !email().trim()) || captcha.blocksSubmit())
      return false
    if (effectiveMethod() === 'passkey')
      return true
    return passwordCanSubmit(pwProps)
  }

  return (
    <>
      {props.header}
      <form class="vstack gap-4" onSubmit={handleSubmit}>
        <UsernameField value={username} onInput={handleUsernameInput} />
        <label>
          Display Name
          <input
            type="text"
            value={displayName()}
            onInput={(e) => {
              setDisplayNameEdited(true)
              setDisplayName(e.currentTarget.value)
            }}
          />
        </label>
        <label>
          Email
          <input
            type="email"
            value={email()}
            onInput={e => setEmail(e.currentTarget.value)}
            required={emailRequired()}
            placeholder={emailRequired()
              ? undefined
              : 'Optional: enables account recovery'}
          />
        </label>
        {/*
          Offered on every sign-up surface, `/setup` included: the hub accepts
          a passkey for the first administrator too, and creates that account
          as an admin exactly as password sign-up does. The shared method
          control drops passkey when this hub runs no ceremonies here.
        */}
        <AuthMethodPillGroup
          label="Sign-up method"
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
        <CaptchaSection action={captchaAction()} captcha={captcha} />
        <Show when={error()}>
          <div class={errorText}>{error()}</div>
        </Show>
        <div class={actionsFooter}>
          <button type="submit" disabled={submitting() || !canSubmit()}>
            <Show when={submitting()}><Spinner /></Show>
            {submitting()
              ? props.submittingLabel
              : effectiveMethod() === 'passkey'
                ? 'Sign up with passkey'
                : props.submitLabel}
          </button>
        </div>
      </form>
    </>
  )
}
