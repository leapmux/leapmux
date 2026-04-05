import type { Component } from 'solid-js'
import type { OAuthProviderInfo } from '~/generated/leapmux/v1/auth_pb'

import { A, useNavigate } from '@solidjs/router'
import { createSignal, onMount, Show } from 'solid-js'
import { OAuthProviderList } from '~/components/common/OAuthProviderList'
import { useAuth } from '~/context/AuthContext'
import { isSignupEnabled, loadOAuthProviders } from '~/lib/systemInfo'
import { cardNarrow } from '~/styles/shared.css'
import * as styles from './LoginPage.css'
import { NotFoundPage } from './NotFoundPage'
import { SignupForm } from './SignupForm'

export const SignupPage: Component = () => {
  const navigate = useNavigate()
  const auth = useAuth()
  const [ready, setReady] = createSignal(false)
  const [verificationSent, setVerificationSent] = createSignal(false)
  const [oauthProviders, setOAuthProviders] = createSignal<OAuthProviderInfo[]>([])

  onMount(async () => {
    setOAuthProviders(await loadOAuthProviders())
    setReady(true)
  })

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
              <SignupForm
                submitLabel="Sign up"
                submittingLabel="Signing up..."
                header={(
                  <Show when={oauthProviders().length > 0}>
                    <OAuthProviderList
                      providers={oauthProviders()}
                      verb="Sign up with"
                      dividerText="or create an account with email"
                    />
                  </Show>
                )}
                onSuccess={(resp, slug) => {
                  if (resp.verificationRequired) {
                    setVerificationSent(true)
                  }
                  else {
                    auth.setAuth(resp.user!)
                    navigate(`/o/${slug}`, { replace: true })
                  }
                }}
              />
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
