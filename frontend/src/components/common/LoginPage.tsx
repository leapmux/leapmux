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
import { authMethodOptions, createAuthMethodSelection } from '~/lib/authMethodSelection'
import { createCaptchaForm } from '~/lib/captchaForm'
import { postAuthNavigate } from '~/lib/postAuthNavigate'
import { stringParam } from '~/lib/searchParam'
import { isEmailEnabled, isSignupEnabled, isSoloMode, loadOAuthProviders } from '~/lib/systemInfo'
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
  // here. This arm covers the visitor who lands on the login form directly
  // (bookmark, typed URL, stale tab) on a SOLO instance, which has no login to
  // offer: the hub authenticates every request, so the form can only fail.
  //
  // The other instance with no login to offer -- one whose first-run setup is
  // not complete -- is answered by `SetupGate` above the router outlet, for
  // every address rather than only this one.
  //
  // Requires `auth.loading()`, and that gate is the whole point. Before
  // the first system-info load `isSoloMode()` answers a fabricated default
  // (`false`), not the hub's answer -- so reading it from `onMount`, as this
  // used to, sampled the default on any load that won this race and then
  // never looked again, because onMount runs once. A solo-mode visitor was
  // left on a credential form that cannot succeed, which is exactly the dead
  // end this arm exists to prevent. AuthGuard's copy of the same call is safe
  // only because it sits behind this same gate.
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
    // Through postAuthNavigate, not navigate: a CLI login bounces here with
    // `?redirect=/auth/cli/start...`, and that address belongs to the hub's
    // mux. A client-side transition would render the SPA's 404 page while
    // the CLI waits for a consent screen nobody ever sees.
    postAuthNavigate(navigate, stringParam(searchParams.redirect), '/')
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
    const redirect = stringParam(searchParams.redirect) ?? ''
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
            options={authMethodOptions()}
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
