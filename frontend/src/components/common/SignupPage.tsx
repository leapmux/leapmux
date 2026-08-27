import type { Component } from 'solid-js'
import type { OAuthProviderInfo } from '~/generated/leapmux/v1/auth_pb'

import { A, useNavigate, useSearchParams } from '@solidjs/router'
import { createSignal, onMount, Show } from 'solid-js'
import { OAuthProviderList } from '~/components/common/OAuthProviderList'
import { useAuth } from '~/context/AuthContext'
import { postAuthNavigate } from '~/lib/postAuthNavigate'
import { stringParam } from '~/lib/searchParam'
import { isSignupEnabled, loadOAuthProviders } from '~/lib/systemInfo'
import { pageCard } from '~/styles/shared.css'
import * as styles from './LoginPage.css'
import { NotFoundPage } from './NotFoundPage'
import { SignupForm } from './SignupForm'

export const SignupPage: Component = () => {
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
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
            title="Sign-up disabled"
            message="New account registration is not currently available."
            linkHref="/login"
            linkText="Go to login"
          />
        )}
      >
        <div class={styles.container}>
          <div class={pageCard}>
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
                onSuccess={(resp) => {
                  // The signup RPC creates a session even when verification
                  // is required, so the user can call the authenticated
                  // VerifyEmail RPC directly. Send them to the verify page to
                  // click the email link or paste the code.
                  auth.setAuth(resp.user)
                  if (resp.verificationRequired) {
                    auth.setVerificationResendAvailableAt(resp.nextResendAvailableAt)
                    setVerificationSent(true)
                    navigate('/verify-email', { replace: true })
                    return
                  }
                  // Same redirect contract as login (see safeRedirect):
                  // a safe in-app path wins, anything else goes home.
                  postAuthNavigate(navigate, stringParam(searchParams.redirect), '/')
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
