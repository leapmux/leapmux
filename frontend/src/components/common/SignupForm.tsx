import type { Timestamp } from '@bufbuild/protobuf/wkt'
import type { Component, JSX } from 'solid-js'

import type { User } from '~/generated/leapmux/v1/auth_pb'
import { createSignal, Show } from 'solid-js'
import { authClient } from '~/api/clients'
import { CaptchaSection } from '~/components/common/CaptchaSection'
import { PillGroup } from '~/components/common/PillGroup'
import { createCaptchaForm } from '~/lib/captchaForm'
import { formatErrorMessage } from '~/lib/errors'
import { isEmailEnabled, isPasskeyEnabled } from '~/lib/systemInfo'
import { sanitizeDisplayName, sanitizeSlug, validateEmail, validateReservedUsername } from '~/lib/validate'
import { startRegistration } from '~/lib/webauthn'
import { errorText } from '~/styles/shared.css'
import { passwordCanSubmit, PasswordFields } from './PasswordFields'
import { Spinner } from './Spinner'
import { UsernameField } from './UsernameField'

type SignupMethod = 'password' | 'passkey'

/**
 * What a successful sign-up hands its caller. `user` is always set on a
 * successful RPC; `verificationEmailSent` is display noise the callers never
 * read, so it stays out of the contract.
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
  /**
   * When true, only password signup is offered (first-admin /setup). Passkey
   * signup is refused by the server during initial setup.
   */
  passwordOnly?: boolean
  onSuccess: (resp: SignupResult) => void
}

export const SignupForm: Component<SignupFormProps> = (props) => {
  const [username, setUsername] = createSignal('')
  const [password, setPassword] = createSignal('')
  const [confirmPassword, setConfirmPassword] = createSignal('')
  const [displayName, setDisplayName] = createSignal('')
  const [displayNameEdited, setDisplayNameEdited] = createSignal(false)
  const [email, setEmail] = createSignal('')
  const [signupMethod, setSignupMethod] = createSignal<SignupMethod>('password')
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

  // The passkey pill renders only when the hub can run ceremonies; a stale
  // passkey selection falls back to the password arm.
  const effectiveMethod = (): SignupMethod => {
    return signupMethod() === 'passkey' && isPasskeyEnabled() ? 'passkey' : 'password'
  }

  const handleSubmit = async (e: Event) => {
    e.preventDefault()
    const fields = validateCommonFields()
    if (!fields)
      return
    if (signupMethod() === 'password' && !passwordCanSubmit(pwProps))
      return
    setSubmitting(true)
    setError(null)
    try {
      if (effectiveMethod() === 'passkey') {
        const begin = await authClient.beginPasskeySignUp({
          username: fields.slug,
          displayName: fields.sanitizedDisplayName,
          email: fields.email,
          ...captcha.fields(),
        })
        const credentialJson = await startRegistration(begin.optionsJson)
        const resp = await authClient.finishPasskeySignUp({
          sessionId: begin.sessionId,
          credentialJson,
        })
        if (!resp.user)
          throw new Error('sign-up response missing user')
        props.onSuccess({
          user: resp.user,
          verificationRequired: resp.emailVerification?.verificationRequired ?? false,
          nextResendAvailableAt: resp.emailVerification?.nextResendAvailableAt,
        })
      }
      else {
        const resp = await authClient.signUp({
          username: fields.slug,
          password: password(),
          displayName: fields.sanitizedDisplayName,
          email: fields.email,
          ...captcha.fields(),
        })
        if (!resp.user)
          throw new Error('sign-up response missing user')
        props.onSuccess({
          user: resp.user,
          verificationRequired: resp.emailVerification?.verificationRequired ?? false,
          nextResendAvailableAt: resp.emailVerification?.nextResendAvailableAt,
        })
      }
    }
    catch (err) {
      setError(formatErrorMessage(err, props.errorPrefix ?? 'Sign up failed'))
      captcha.reset(err)
      setSubmitting(false)
    }
  }

  const captchaAction = () => effectiveMethod() === 'passkey' ? 'passkey_signup' as const : 'signup' as const

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
        <Show when={!props.passwordOnly}>
          <PillGroup
            label="Sign-up method"
            options={[
              { value: 'password' as const, label: 'Password' },
              ...(isPasskeyEnabled() ? [{ value: 'passkey' as const, label: 'Passkey' }] : []),
            ]}
            selected={v => effectiveMethod() === v}
            onSelect={setSignupMethod}
          />
        </Show>
        <Show when={signupMethod() === 'password' || props.passwordOnly}>
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
        <button type="submit" disabled={submitting() || !canSubmit()}>
          <Show when={submitting()}><Spinner /></Show>
          {submitting()
            ? props.submittingLabel
            : effectiveMethod() === 'passkey' ? 'Sign up with passkey' : props.submitLabel}
        </button>
      </form>
    </>
  )
}
