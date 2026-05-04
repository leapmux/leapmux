import type { Component } from 'solid-js'

import { A, useNavigate, useSearchParams } from '@solidjs/router'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createSignal, onMount, Show } from 'solid-js'
import { authClient } from '~/api/clients'
import { Icon } from '~/components/common/Icon'
import * as styles from '~/components/common/LoginPage.css'
import { UsernameField } from '~/components/common/UsernameField'
import { useAuth } from '~/context/AuthContext'
import { setPageTitle } from '~/lib/pageTitle'
import { sanitizeDisplayName, sanitizeSlug, validateReservedUsername } from '~/lib/validate'
import { spinner } from '~/styles/animations.css'
import { cardNarrow, errorText } from '~/styles/shared.css'

const OAuthCompleteSignupPage: Component = () => {
  const navigate = useNavigate()
  const auth = useAuth()
  const [searchParams] = useSearchParams()
  const signupToken = () => typeof searchParams.token === 'string' ? searchParams.token : ''

  const [username, setUsername] = createSignal('')
  const [displayName, setDisplayName] = createSignal('')
  const [email, setEmail] = createSignal('')
  const [providerName, setProviderName] = createSignal('')
  const [submitting, setSubmitting] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)
  const [loading, setLoading] = createSignal(true)
  const [tokenError, setTokenError] = createSignal<string | null>(null)

  onMount(async () => {
    setPageTitle('Complete Sign Up')
    if (!signupToken()) {
      setTokenError('Missing signup token.')
      setLoading(false)
      return
    }
    try {
      const resp = await authClient.getPendingOAuthSignup({ signupToken: signupToken() })
      setDisplayName(resp.displayName)
      setEmail(resp.email)
      setProviderName(resp.providerName)
    }
    catch {
      setTokenError('This signup link is invalid or has expired.')
    }
    finally {
      setLoading(false)
    }
  })

  const handleSubmit = async (e: Event) => {
    e.preventDefault()
    const [slug, slugErr] = sanitizeSlug('Username', username())
    if (slugErr) {
      setError(slugErr)
      return
    }
    // OAuth completion is a post-authentication flow; always treat it as
    // public signup (admin is reserved, setup uses the /setup page instead).
    const reservedErr = validateReservedUsername(slug, false)
    if (reservedErr) {
      setError(reservedErr)
      return
    }
    const { value: sanitizedDisplayName, error: dnErr } = sanitizeDisplayName(displayName(), slug)
    if (dnErr) {
      setError(dnErr)
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      const resp = await authClient.completeOAuthSignup({
        signupToken: signupToken(),
        username: slug,
        displayName: sanitizedDisplayName,
      })
      auth.setAuth(resp.user!)
      // OAuth signup mirrors the SignUp flow: when the provider returned
      // an unverified email and verification is enabled, send the user
      // to /verify-email so they can paste the code (or click through).
      // The session was created server-side, so the authenticated
      // VerifyEmail RPC is reachable from there.
      if (resp.verificationRequired) {
        navigate('/verify-email', { replace: true })
      }
      else {
        navigate(`/o/${slug}`, { replace: true })
      }
    }
    catch (e) {
      setError(e instanceof Error ? e.message : 'Sign up failed')
      setSubmitting(false)
    }
  }

  return (
    <div class={styles.container}>
      <div class={`card ${cardNarrow}`}>
        <h1>Complete Sign Up</h1>
        <Show when={loading()}>
          <div class={styles.loadingCenter}>
            <Icon icon={LoaderCircle} size="md" class={spinner} />
          </div>
        </Show>
        <Show when={!loading() && tokenError()}>
          <div class={errorText}>{tokenError()}</div>
          <div class={styles.authFooter}>
            <A href="/login">Back to login</A>
          </div>
        </Show>
        <Show when={!loading() && !tokenError()}>
          <Show when={providerName()}>
            <p class={styles.providerHint}>
              Signed in via
              {' '}
              {providerName()}
              . Choose a username to finish creating your account.
            </p>
          </Show>
          <form class="vstack gap-4" onSubmit={handleSubmit}>
            <UsernameField value={username} onInput={setUsername} />
            <label>
              Display Name
              <input
                type="text"
                value={displayName()}
                onInput={e => setDisplayName(e.currentTarget.value)}
              />
            </label>
            <Show when={email()}>
              <label>
                Email
                <input
                  type="email"
                  value={email()}
                  readOnly
                />
              </label>
            </Show>
            <Show when={error()}>
              <div class={errorText}>{error()}</div>
            </Show>
            <button type="submit" disabled={submitting() || !username()}>
              <Show when={submitting()}><Icon icon={LoaderCircle} size="sm" class={spinner} /></Show>
              {submitting() ? 'Creating account...' : 'Create account'}
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
  )
}

export default OAuthCompleteSignupPage
