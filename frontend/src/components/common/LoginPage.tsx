import type { Timestamp } from '@bufbuild/protobuf/wkt'
import type { Component } from 'solid-js'

import type { OAuthProviderInfo } from '~/generated/leapmux/v1/auth_pb'
import { A, useNavigate, useSearchParams } from '@solidjs/router'
import { createEffect, createSignal, Show } from 'solid-js'
import { CaptchaSection } from '~/components/common/CaptchaSection'
import { OAuthProviderList } from '~/components/common/OAuthProviderList'
import { PillGroup } from '~/components/common/PillGroup'
import { Spinner } from '~/components/common/Spinner'
import { useAuth } from '~/context/AuthContext'
import { createAuthMethodSelection } from '~/lib/authMethodSelection'
import { createCaptchaForm } from '~/lib/captchaForm'
import { safeRedirect } from '~/lib/safeRedirect'
import { isEmailEnabled, isPasskeyEnabled, isSetupRequired, isSignupEnabled, isSoloMode, loadOAuthProviders } from '~/lib/systemInfo'
import { errorText, pageCard } from '~/styles/shared.css'
import * as styles from './LoginPage.css'

export const LoginPage: Component = () => {
  const auth = useAuth()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const [username, setUsername] = createSignal('')
  const [password, setPassword] = createSignal('')
  const methodSelection = createAuthMethodSelection('login')
  const effectiveMethod = methodSelection.effectiveMethod
  const [submitting, setSubmitting] = createSignal(false)
  const [oauthProviders, setOAuthProviders] = createSignal<OAuthProviderInfo[]>([])
  const captcha = createCaptchaForm()
  let usernameRef!: HTMLInputElement
  let passwordRef!: HTMLInputElement

  // `/login` sits outside the `(app)` layout, so AuthGuard never sees a visit
  // here. These two arms cover the visitor who lands on the login form directly
  // (bookmark, typed URL, stale tab) on an instance that has no login to offer:
  // solo mode, or a fresh install with no account yet.
  //
  // Requires `auth.loading()`, and that gate is the whole point. Before
  // the first system-info load the getters answer fabricated defaults
  // (`soloMode = false`, `setupRequired = false`), not the hub's answers
  // -- so reading them from `onMount`, as this used to, sampled the
  // defaults on any load that won this race and then never looked again,
  // because onMount runs once. A solo-mode visitor was left on a
  // credential form that cannot succeed, which is exactly the dead end
  // these arms exist to prevent. AuthGuard's copies of the same two
  // calls are safe only because they sit behind this same gate.
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
  //
  // The captcha gate does NOT lean on `auth.loading()`: that signal flips on
  // every attempt, which would tear the widget down mid-session. It tracks the
  // system-info signal instead (see createCaptchaForm).
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

  const navigateAfterAuth = (verificationRequired: boolean, nextResendAvailableAt?: Timestamp) => {
    if (verificationRequired) {
      auth.setVerificationResendAvailableAt(nextResendAvailableAt)
      navigate('/verify-email', { replace: true })
      return
    }
    const redirect = safeRedirect(typeof searchParams.redirect === 'string' ? searchParams.redirect : undefined)
    navigate(redirect ?? '/', { replace: true })
  }

  const handleSubmit = async (e: Event) => {
    e.preventDefault()
    setSubmitting(true)
    try {
      const result = effectiveMethod() === 'passkey'
        ? await auth.loginWithPasskey(username(), captcha.fields())
        : await auth.login(username(), password(), captcha.fields())
      if (auth.user())
        navigateAfterAuth(result.verificationRequired, result.nextResendAvailableAt)
      // A success without a user leaves nowhere to navigate; the finally
      // below re-enables the form instead of leaving a spinner with no
      // error and no way to resubmit.
    }
    catch (err) {
      // Error is captured by auth context. A rejected captcha (expired
      // solve, replay) must not linger: force a fresh challenge. Pass
      // the error so PermissionDenied refreshes captcha system-info.
      captcha.reset(err)
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

  const captchaAction = methodSelection.captchaAction

  const canSubmit = () => {
    if (!username() || captcha.blocksSubmit())
      return false
    if (effectiveMethod() === 'passkey')
      return true
    return !!password()
  }

  return (
    <div class={styles.container}>
      <div class={pageCard}>
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
          <PillGroup
            label="Sign-in method"
            options={[
              { value: 'password' as const, label: 'Password' },
              ...(isPasskeyEnabled() ? [{ value: 'passkey' as const, label: 'Passkey' }] : []),
            ]}
            selected={v => effectiveMethod() === v}
            onSelect={methodSelection.select}
          />
          <Show when={effectiveMethod() === 'password'}>
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
          </Show>
          <CaptchaSection action={captchaAction()} captcha={captcha} />
          <Show when={auth.error()}>
            <div class={errorText}>{auth.error()}</div>
          </Show>
          <button
            type="submit"
            disabled={submitting() || !canSubmit()}
          >
            <Show when={submitting()}><Spinner /></Show>
            {submitting()
              ? 'Signing in...'
              : effectiveMethod() === 'passkey' ? 'Sign in with passkey' : 'Sign in'}
          </button>
          <Show when={isEmailEnabled() && effectiveMethod() === 'password'}>
            <div>
              <A href="/forgot-password">Forgot password?</A>
            </div>
          </Show>
        </form>
        <Show when={isSignupEnabled()}>
          <div class={styles.authFooter}>
            <A href="/signup">Sign up</A>
          </div>
        </Show>
      </div>
    </div>
  )
}
