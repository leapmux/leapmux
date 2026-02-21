import type { Component } from 'solid-js'

import { A, useNavigate, useSearchParams } from '@solidjs/router'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createSignal, onMount, Show } from 'solid-js'
import { authClient } from '~/api/clients'
import { useAuth } from '~/context/AuthContext'
import { spinner } from '~/styles/animations.css'
import * as styles from './LoginPage.css'

export const LoginPage: Component = () => {
  const auth = useAuth()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const [username, setUsername] = createSignal('')
  const [password, setPassword] = createSignal('')
  const [submitting, setSubmitting] = createSignal(false)
  const [signupEnabled, setSignupEnabled] = createSignal(false)

  onMount(async () => {
    try {
      const resp = await authClient.getSystemInfo({})
      setSignupEnabled(resp.signupEnabled)
    }
    catch {
      // Ignore - signup link stays hidden
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

  return (
    <div class={styles.container}>
      <div class={`card ${styles.authCard}`}>
        <h1>LeapMux</h1>
        <form class="vstack gap-4" onSubmit={handleSubmit}>
          <label>
            Username
            <input
              type="text"
              value={username()}
              onInput={e => setUsername(e.currentTarget.value)}
              autocomplete="username"
            />
          </label>
          <label>
            Password
            <input
              type="password"
              value={password()}
              onInput={e => setPassword(e.currentTarget.value)}
              autocomplete="current-password"
            />
          </label>
          <Show when={auth.error()}>
            <div class={styles.errorText}>{auth.error()}</div>
          </Show>
          <button
            type="submit"
            disabled={submitting() || !username() || !password()}
          >
            <Show when={submitting()}><LoaderCircle size={14} class={spinner} /></Show>
            {submitting() ? 'Signing in...' : 'Sign in'}
          </button>
        </form>
        <Show when={signupEnabled()}>
          <div class={styles.authFooter}>
            <A href="/signup">Sign up</A>
          </div>
        </Show>
      </div>
    </div>
  )
}
