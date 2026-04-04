import type { Component } from 'solid-js'
import type { OAuthProviderInfo } from '~/generated/leapmux/v1/auth_pb'

import { A, useNavigate, useSearchParams } from '@solidjs/router'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createSignal, For, onMount, Show } from 'solid-js'
import { authClient } from '~/api/clients'
import { Icon } from '~/components/common/Icon'
import { useAuth } from '~/context/AuthContext'
import { isSoloMode } from '~/lib/systemInfo'
import { spinner } from '~/styles/animations.css'
import { cardNarrow, errorText } from '~/styles/shared.css'
import * as styles from './LoginPage.css'

export const LoginPage: Component = () => {
  const auth = useAuth()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const [username, setUsername] = createSignal('')
  const [password, setPassword] = createSignal('')
  const [submitting, setSubmitting] = createSignal(false)
  const [signupEnabled, setSignupEnabled] = createSignal(false)
  const [oauthProviders, setOAuthProviders] = createSignal<OAuthProviderInfo[]>([])
  let usernameRef!: HTMLInputElement
  let passwordRef!: HTMLInputElement

  onMount(async () => {
    if (isSoloMode()) {
      navigate('/o/admin', { replace: true })
      return
    }

    // Focus the first empty input field (username if both empty).
    if (!usernameRef.value) {
      usernameRef.focus()
    }
    else if (!passwordRef.value) {
      passwordRef.focus()
    }

    try {
      const resp = await authClient.getSystemInfo({})
      setSignupEnabled(resp.signupEnabled)
    }
    catch {
      // Ignore - signup link stays hidden
    }

    // Fetch enabled OAuth providers.
    try {
      const resp = await authClient.getOAuthProviders({})
      setOAuthProviders(resp.providers)
    }
    catch {
      // Ignore - OAuth buttons stay hidden
    }
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
          navigate(`/o/${user.username}`, { replace: true })
        }
      }
    }
    catch {
      // Error is captured by auth context.
    }
    finally {
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
          <div class="vstack gap-2">
            <For each={oauthProviders()}>
              {provider => (
                <a
                  href={oauthLoginUrl(provider)}
                  class={styles.oauthButton}
                >
                  Sign in with
                  {' '}
                  {provider.name}
                </a>
              )}
            </For>
          </div>
          <div class={styles.divider}>
            <span>or</span>
          </div>
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
            <Show when={submitting()}><Icon icon={LoaderCircle} size="sm" class={spinner} /></Show>
            {submitting() ? 'Signing in...' : 'Sign in'}
          </button>
        </form>
        <Show when={signupEnabled() || oauthProviders().length > 0}>
          <div class={styles.authFooter}>
            <Show when={signupEnabled()}>
              <A href="/signup">Sign up</A>
            </Show>
          </div>
        </Show>
      </div>
    </div>
  )
}
