import type { Component } from 'solid-js'

import { useNavigate } from '@solidjs/router'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createSignal, onMount, Show } from 'solid-js'
import { authClient } from '~/api/clients'
import { Icon } from '~/components/common/Icon'
import { passwordCanSubmit, PasswordFields } from '~/components/common/PasswordFields'
import { UsernameField } from '~/components/common/UsernameField'
import { useAuth } from '~/context/AuthContext'
import { clearSetupRequired, isSetupRequired } from '~/lib/systemInfo'
import { sanitizeDisplayName, sanitizeSlug, validateEmail } from '~/lib/validate'
import { spinner } from '~/styles/animations.css'
import { cardNarrow, errorText } from '~/styles/shared.css'
import * as styles from './LoginPage.css'

export const SetupPage: Component = () => {
  const navigate = useNavigate()
  const auth = useAuth()
  const [username, setUsername] = createSignal('')
  const [password, setPassword] = createSignal('')
  const [confirmPassword, setConfirmPassword] = createSignal('')
  const [displayName, setDisplayName] = createSignal('')
  const [email, setEmail] = createSignal('')
  const [submitting, setSubmitting] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)
  const [ready, setReady] = createSignal(false)

  onMount(() => {
    if (!isSetupRequired()) {
      navigate('/login', { replace: true })
      return
    }
    setReady(true)
  })

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
      clearSetupRequired()
      auth.setAuth(resp.user!)
      navigate(`/o/${slug}`, { replace: true })
    }
    catch (e) {
      setError(e instanceof Error ? e.message : 'Setup failed')
    }
    finally {
      setSubmitting(false)
    }
  }

  return (
    <Show when={ready()} fallback={null}>
      <div class={styles.container}>
        <div class={`card ${cardNarrow}`}>
          <h1>Create Administrator Account</h1>
          <p>Welcome to LeapMux. Create the first administrator account to get started.</p>
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
              {submitting() ? 'Creating account...' : 'Create account'}
            </button>
          </form>
        </div>
      </div>
    </Show>
  )
}
