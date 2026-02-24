import type { Component } from 'solid-js'

import { A, useNavigate } from '@solidjs/router'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createSignal, onMount, Show } from 'solid-js'
import { authClient } from '~/api/clients'
import { useAuth } from '~/context/AuthContext'
import { sanitizeSlug } from '~/lib/validate'
import { spinner } from '~/styles/animations.css'
import * as styles from './LoginPage.css'
import { NotFoundPage } from './NotFoundPage'

export const SignupPage: Component = () => {
  const navigate = useNavigate()
  const auth = useAuth()
  const [username, setUsername] = createSignal('')
  const [password, setPassword] = createSignal('')
  const [confirmPassword, setConfirmPassword] = createSignal('')
  const [displayName, setDisplayName] = createSignal('')
  const [email, setEmail] = createSignal('')
  const [submitting, setSubmitting] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)
  const [verificationSent, setVerificationSent] = createSignal(false)
  const [signupEnabled, setSignupEnabled] = createSignal<boolean | null>(null)

  onMount(async () => {
    try {
      const resp = await authClient.getSystemInfo({})
      setSignupEnabled(resp.signupEnabled)
    }
    catch {
      setSignupEnabled(false)
    }
  })

  const handleSubmit = async (e: Event) => {
    e.preventDefault()
    if (password() !== confirmPassword()) {
      setError('Passwords do not match.')
      return
    }
    const [slug, slugErr] = sanitizeSlug('Username', username())
    if (slugErr) {
      setError(slugErr)
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      const resp = await authClient.signUp({
        username: slug,
        password: password(),
        displayName: displayName(),
        email: email(),
      })
      if (resp.verificationRequired) {
        setVerificationSent(true)
      }
      else {
        auth.setAuth(resp.token, resp.user!)
        navigate(`/o/${slug}`, { replace: true })
      }
    }
    catch (e) {
      setError(e instanceof Error ? e.message : 'Sign up failed')
    }
    finally {
      setSubmitting(false)
    }
  }

  return (
    <Show when={signupEnabled() !== null} fallback={null}>
      <Show
        when={signupEnabled()}
        fallback={(
          <NotFoundPage
            title="Sign Up Disabled"
            message="New account registration is not currently available."
            linkHref="/login"
            linkText="Go to login"
          />
        )}
      >
        <div class={styles.container}>
          <div class={`card ${styles.authCard}`}>
            <h1>Sign Up</h1>
            <Show when={verificationSent()}>
              <div class={styles.verificationMessage}>
                Check your email to verify your account.
                <br />
                <A href="/login" class={styles.inlineLink}>Back to login</A>
              </div>
            </Show>
            <Show when={!verificationSent()}>
              <form class="vstack gap-4" onSubmit={handleSubmit}>
                <label>
                  Username
                  <input type="text" value={username()} onInput={e => setUsername(e.currentTarget.value)} autocomplete="username" />
                </label>
                <label>
                  Display Name
                  <input type="text" value={displayName()} onInput={e => setDisplayName(e.currentTarget.value)} />
                </label>
                <label>
                  Email
                  <input type="email" value={email()} onInput={e => setEmail(e.currentTarget.value)} />
                </label>
                <label>
                  Password
                  <input type="password" value={password()} onInput={e => setPassword(e.currentTarget.value)} autocomplete="new-password" />
                </label>
                <label>
                  Confirm Password
                  <input type="password" value={confirmPassword()} onInput={e => setConfirmPassword(e.currentTarget.value)} autocomplete="new-password" />
                </label>
                <Show when={error()}>
                  <div class={styles.errorText}>{error()}</div>
                </Show>
                <button type="submit" disabled={submitting() || !username() || !password()}>
                  <Show when={submitting()}><LoaderCircle size={14} class={spinner} /></Show>
                  {submitting() ? 'Signing up...' : 'Sign up'}
                </button>
              </form>
              <div class={styles.authFooter}>
                Already have an account?
                {' '}
                <A href="/login">Sign in</A>
              </div>
            </Show>
          </div>
        </div>
      </Show>
    </Show>
  )
}
