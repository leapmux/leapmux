import type { Component } from 'solid-js'
import type { OAuthProviderInfo } from '~/generated/leapmux/v1/auth_pb'

import { A, useNavigate, useSearchParams } from '@solidjs/router'
import { createEffect, createSignal, Show } from 'solid-js'
import { OAuthProviderList } from '~/components/common/OAuthProviderList'
import { Spinner } from '~/components/common/Spinner'
import { useAuth } from '~/context/AuthContext'
import { isSetupRequired, isSignupEnabled, isSoloMode, loadOAuthProviders } from '~/lib/systemInfo'
import { cardNarrow, errorText } from '~/styles/shared.css'
import * as styles from './LoginPage.css'

export const LoginPage: Component = () => {
  const auth = useAuth()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const [username, setUsername] = createSignal('')
  const [password, setPassword] = createSignal('')
  const [submitting, setSubmitting] = createSignal(false)
  const [oauthProviders, setOAuthProviders] = createSignal<OAuthProviderInfo[]>([])
  let usernameRef!: HTMLInputElement
  let passwordRef!: HTMLInputElement

  // `/login` sits outside the `(app)` layout, so AuthGuard never sees a visit
  // here. These two arms cover the visitor who lands on the login form directly
  // (bookmark, typed URL, stale tab) on an instance that has no login to offer:
  // solo mode, or a fresh install with no account yet.
  //
  // Gated on `auth.loading()`, and that gate is the whole point. The system-info
  // getters are plain module variables whose pre-fetch values are FABRICATIONS
  // (`soloMode = false`, `setupRequired = false`), not signals -- so reading
  // them from `onMount`, as this used to, sampled the defaults on any load that
  // won this race and then never looked again, because onMount runs once. A
  // solo-mode visitor was left on a credential form that cannot succeed, which
  // is exactly the dead end these arms exist to prevent. AuthGuard's copies of
  // the same two calls are safe only because they sit behind this same gate.
  //
  // createEffect, not onMount: it re-runs when `auth.loading()` flips, which is
  // the earliest moment the getters are answers rather than guesses.
  //
  // The effect body stays SYNCHRONOUS. Solid tracks reads only up to the first
  // await, so an `async` effect silently stops tracking anything after it --
  // here that would have made `auth.loading()` the only reliable dependency by
  // accident rather than by design. The one async step (fetching OAuth
  // providers) is a fire-and-forget continuation at the end, which needs no
  // tracking: it reads nothing reactive and only writes.
  let bootstrapped = false
  createEffect(() => {
    if (auth.loading() || bootstrapped) {
      return
    }
    bootstrapped = true

    if (isSoloMode()) {
      navigate('/', { replace: true })
      return
    }
    if (isSetupRequired()) {
      navigate('/setup', { replace: true })
      return
    }

    // Focus the first empty input field (username if both empty).
    if (!usernameRef.value) {
      usernameRef.focus()
    }
    else if (!passwordRef.value) {
      passwordRef.focus()
    }

    void loadOAuthProviders().then(setOAuthProviders)
  })

  const handleSubmit = async (e: Event) => {
    e.preventDefault()
    setSubmitting(true)
    try {
      await auth.login(username(), password())
      const user = auth.user()
      if (user) {
        const redirect = typeof searchParams.redirect === 'string' ? searchParams.redirect : undefined
        if (redirect && redirect.startsWith('/') && !redirect.startsWith('//')) {
          navigate(redirect, { replace: true })
        }
        else {
          navigate('/', { replace: true })
        }
      }
    }
    catch {
      // Error is captured by auth context.
      setSubmitting(false)
    }
  }

  const oauthLoginUrl = (provider: OAuthProviderInfo) => {
    const redirect = typeof searchParams.redirect === 'string' ? searchParams.redirect : ''
    const url = provider.loginUrl
    if (redirect) {
      return `${url}?redirect=${encodeURIComponent(redirect)}`
    }
    return url
  }

  return (
    <div class={styles.container}>
      <div class={`card ${cardNarrow}`}>
        <h1>LeapMux</h1>
        <Show when={oauthProviders().length > 0}>
          <OAuthProviderList
            providers={oauthProviders()}
            verb="Sign in with"
            dividerText="or"
            buildUrl={oauthLoginUrl}
          />
        </Show>
        <form class="vstack gap-4" onSubmit={handleSubmit}>
          <label>
            Username
            <input
              ref={usernameRef}
              type="text"
              value={username()}
              onInput={e => setUsername(e.currentTarget.value)}
              autocomplete="username"
            />
          </label>
          <label>
            Password
            <input
              ref={passwordRef}
              type="password"
              value={password()}
              onInput={e => setPassword(e.currentTarget.value)}
              autocomplete="current-password"
            />
          </label>
          <Show when={auth.error()}>
            <div class={errorText}>{auth.error()}</div>
          </Show>
          <button
            type="submit"
            disabled={submitting() || !username() || !password()}
          >
            <Show when={submitting()}><Spinner /></Show>
            {submitting() ? 'Signing in...' : 'Sign in'}
          </button>
        </form>
        {/* Reads auth.loading() so this re-evaluates once bootstrap lands.
            isSignupEnabled() is a plain module read with no reactivity of its
            own, so without that dependency the link stayed frozen at the
            pre-fetch `false` and never appeared on a direct /login load. */}
        <Show when={!auth.loading() && isSignupEnabled()}>
          <div class={styles.authFooter}>
            <A href="/signup">Sign up</A>
          </div>
        </Show>
      </div>
    </div>
  )
}
