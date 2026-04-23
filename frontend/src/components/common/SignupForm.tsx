import type { Component, JSX } from 'solid-js'
import type { SignUpResponse } from '~/generated/leapmux/v1/auth_pb'

import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createSignal, Show } from 'solid-js'
import { authClient } from '~/api/clients'
import { sanitizeDisplayName, sanitizeSlug, validateEmail, validateReservedUsername } from '~/lib/validate'
import { spinner } from '~/styles/animations.css'
import { errorText } from '~/styles/shared.css'
import { Icon } from './Icon'
import { passwordCanSubmit, PasswordFields } from './PasswordFields'
import { UsernameField } from './UsernameField'

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
  onSuccess: (resp: SignUpResponse, username: string) => void
}

export const SignupForm: Component<SignupFormProps> = (props) => {
  const [username, setUsername] = createSignal('')
  const [password, setPassword] = createSignal('')
  const [confirmPassword, setConfirmPassword] = createSignal('')
  const [displayName, setDisplayName] = createSignal('')
  const [email, setEmail] = createSignal('')
  const [submitting, setSubmitting] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)

  const pwProps = { password, confirmPassword }

  const handleSubmit = async (e: Event) => {
    e.preventDefault()
    if (!passwordCanSubmit(pwProps))
      return
    const [slug, slugErr] = sanitizeSlug('Username', username())
    if (slugErr) {
      setError(slugErr)
      return
    }
    const reservedErr = validateReservedUsername(slug, props.allowAdminUsername ?? false)
    if (reservedErr) {
      setError(reservedErr)
      return
    }
    const { value: sanitizedDisplayName, error: dnErr } = sanitizeDisplayName(displayName(), slug)
    if (dnErr) {
      setError(dnErr)
      return
    }
    const emailErr = validateEmail(email())
    if (emailErr) {
      setError(emailErr)
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      const resp = await authClient.signUp({
        username: slug,
        password: password(),
        displayName: sanitizedDisplayName,
        email: email(),
      })
      props.onSuccess(resp, slug)
    }
    catch (e) {
      setError(e instanceof Error ? e.message : (props.errorPrefix ?? 'Sign up failed'))
      setSubmitting(false)
    }
  }

  return (
    <>
      {props.header}
      <form class="vstack gap-4" onSubmit={handleSubmit}>
        <UsernameField value={username} onInput={setUsername} />
        <label>
          Display Name
          <input type="text" value={displayName()} onInput={e => setDisplayName(e.currentTarget.value)} />
        </label>
        <label>
          Email
          <input type="email" value={email()} onInput={e => setEmail(e.currentTarget.value)} />
        </label>
        <PasswordFields
          password={password}
          setPassword={setPassword}
          confirmPassword={confirmPassword}
          setConfirmPassword={setConfirmPassword}
        />
        <Show when={error()}>
          <div class={errorText}>{error()}</div>
        </Show>
        <button type="submit" disabled={submitting() || !username() || !passwordCanSubmit(pwProps)}>
          <Show when={submitting()}><Icon icon={LoaderCircle} size="sm" class={spinner} /></Show>
          {submitting() ? props.submittingLabel : props.submitLabel}
        </button>
      </form>
    </>
  )
}
