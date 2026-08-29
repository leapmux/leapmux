import type { Timestamp } from '@bufbuild/protobuf/wkt'
import type { Component, JSX } from 'solid-js'

import type { EmailVerificationStatus, User } from '~/generated/leapmux/v1/auth_pb'
import { createSignal, Show } from 'solid-js'
import { authClient } from '~/api/clients'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { CaptchaSection } from '~/components/common/CaptchaSection'
import { PillGroup } from '~/components/common/PillGroup'
import { authMethodOptions, createAuthMethodSelection } from '~/lib/authMethodSelection'
import { createCaptchaForm } from '~/lib/captchaForm'
import { isEmailEnabled } from '~/lib/systemInfo'
import { sanitizeDisplayName, sanitizeSlug, validateEmail, validateReservedUsername } from '~/lib/validate'
import { passkeyErrorMessage, startRegistration } from '~/lib/webauthn'
import { errorText } from '~/styles/shared.css'
import { passwordCanSubmit, PasswordFields } from './PasswordFields'
import { Spinner } from './Spinner'
import { UsernameField } from './UsernameField'

/**
 * What a successful sign-up hands its caller. `user` is always set on a
 * successful RPC; `verificationEmailSent` is a display detail that the callers
 * never read, so it stays out of the contract.
 */
export interface SignupResult {
  user: User
  verificationRequired: boolean
  nextResendAvailableAt?: Timestamp
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
  onSuccess: (resp: SignupResult) => void
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
      let resp: { user?: User, emailVerification?: EmailVerificationStatus }
      if (effectiveMethod() === 'passkey') {
        const begin = await authClient.beginPasskeySignUp({
          username: fields.slug,
          displayName: fields.sanitizedDisplayName,
          email: fields.email,
          ...captcha.fields(),
        })
        const credentialJson = await startRegistration(begin.optionsJson)
        const passkeyResp = await authClient.finishPasskeySignUp({
          sessionId: begin.sessionId,
          credentialJson,
        })
        resp = passkeyResp
      }
      else {
        resp = await authClient.signUp({
          username: fields.slug,
          password: password(),
          displayName: fields.sanitizedDisplayName,
          email: fields.email,
          ...captcha.fields(),
        })
      }
      // One success path for both branches: the two responses carry the same
      // two fields, and the `?? false` defaults were spelled twice.
      if (!resp.user)
        throw new Error('sign-up response missing user')
      props.onSuccess({
        user: resp.user,
        verificationRequired: resp.emailVerification?.verificationRequired ?? false,
        nextResendAvailableAt: resp.emailVerification?.nextResendAvailableAt,
      })
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
            placeholder={emailRequired() ? undefined : 'Optional: enables password reset'}
          />
        </label>
        {/*
          Offered on every sign-up surface, `/setup` included: the hub accepts
          a passkey for the first administrator too, and creates that account
          as an admin exactly as password sign-up does. `authMethodOptions`
          decides what the pills show, and it drops the passkey option on a
          hub that runs no ceremonies at this origin.
        */}
        <PillGroup
          label="Sign-up method"
          options={authMethodOptions()}
          selected={v => effectiveMethod() === v}
          onSelect={methodSelection.select}
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
              : effectiveMethod() === 'passkey' ? 'Sign up with passkey' : props.submitLabel}
          </button>
        </div>
      </form>
    </>
  )
}
