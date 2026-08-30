import type { Timestamp } from '@bufbuild/protobuf/wkt'
import type { Component } from 'solid-js'

import type { OAuthProviderInfo } from '~/generated/proto/leapmux/v1/auth_pb'
import { A, useNavigate, useSearchParams } from '@solidjs/router'
import { createEffect, createSignal, Show } from 'solid-js'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { CaptchaSection } from '~/components/common/CaptchaSection'
import { OAuthProviderList } from '~/components/common/OAuthProviderList'
import { PillGroup } from '~/components/common/PillGroup'
import { Spinner } from '~/components/common/Spinner'
import { useAuth } from '~/context/AuthContext'
import { authMethodOptions, createAuthMethodSelection } from '~/lib/authMethodSelection'
import { createCaptchaForm } from '~/lib/captchaForm'
import { postAuthNavigate } from '~/lib/postAuthNavigate'
import { stringParam } from '~/lib/searchParam'
import { isEmailEnabled, isSignupEnabled, loadOAuthProviders } from '~/lib/systemInfo'
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

  // Two guards ABOVE this page answer the two hubs with no login to offer. A solo
  // hub authenticates every request, so `SignedOutOnly` sends that visitor to
  // the app; a hub whose first-run setup is not complete has no account, so
  // `SetupGate` sends them to `/setup`. Both guards cover every credential
  // address rather than this one alone.
  //
  // What stays here is the page's own setup, and it still requires
  // `auth.loading()`. Before the first system-info load the getters answer
  // fabricated defaults -- `isSignupEnabled()` reads `false` -- so an
  // `onMount` that sampled them on a load which won the race pinned the signup
  // link off and never looked again.
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
  // The captcha gate does NOT depend on `auth.loading()`: that signal flips on
  // every attempt, which would tear the widget down mid-session. It tracks the
  // system-info signal instead (see createCaptchaForm).
  let bootstrapped = false
  createEffect(() => {
    if (auth.loading() || bootstrapped) {
      return
    }
    bootstrapped = true

    // The solo rule is NOT here. `SignedOutOnly` carries it for all five
    // credential pages, so this page no longer keeps a copy that reached one.
    //
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
    // `?redirect=/oauth/authorize...`, and that address belongs to the hub's
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
      // The auth context captures the error. A rejected captcha (expired
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
          <div class={actionsFooter}>
            <button
              type="submit"
              disabled={submitting() || !canSubmit()}
            >
              <Show when={submitting()}><Spinner /></Show>
              {submitting()
                ? 'Signing in...'
                : effectiveMethod() === 'passkey' ? 'Sign in with passkey' : 'Sign in'}
            </button>
          </div>
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
