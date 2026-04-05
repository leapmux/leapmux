import type { Component } from 'solid-js'
import type { OAuthProviderInfo } from '~/generated/leapmux/v1/auth_pb'

import { A, useNavigate } from '@solidjs/router'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createSignal, onMount, Show } from 'solid-js'
import { authClient } from '~/api/clients'
import { Icon } from '~/components/common/Icon'
import { OAuthProviderList } from '~/components/common/OAuthProviderList'
import { passwordCanSubmit, PasswordFields } from '~/components/common/PasswordFields'
import { UsernameField } from '~/components/common/UsernameField'
import { useAuth } from '~/context/AuthContext'
import { isSignupEnabled, loadOAuthProviders } from '~/lib/systemInfo'
import { sanitizeDisplayName, sanitizeSlug, validateEmail } from '~/lib/validate'
import { spinner } from '~/styles/animations.css'
import { cardNarrow, errorText } from '~/styles/shared.css'
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
  const [ready, setReady] = createSignal(false)
  const [oauthProviders, setOAuthProviders] = createSignal<OAuthProviderInfo[]>([])

  onMount(async () => {
    setOAuthProviders(await loadOAuthProviders())
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
      if (resp.verificationRequired) {
        setVerificationSent(true)
      }
      else {
        auth.setAuth(resp.user!)
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
    <Show when={ready()} fallback={null}>
      <Show
        when={isSignupEnabled()}
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
          <div class={`card ${cardNarrow}`}>
            <h1>Sign Up</h1>
            <Show when={verificationSent()}>
              <div class={styles.verificationMessage}>
                Check your email to verify your account.
                <br />
                <A href="/login" class={styles.inlineLink}>Back to login</A>
              </div>
            </Show>
            <Show when={!verificationSent()}>
              <Show when={oauthProviders().length > 0}>
                <OAuthProviderList
                  providers={oauthProviders()}
                  verb="Sign up with"
                  dividerText="or create an account with email"
                />
              </Show>
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
