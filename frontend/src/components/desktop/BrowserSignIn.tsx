import type { Component } from 'solid-js'
import { useNavigate, useSearchParams } from '@solidjs/router'
import { Show } from 'solid-js'
import { openExternalUrl, platformBridge } from '~/api/platformBridge'
import { useAuth } from '~/context/AuthContext'
import { copyTextToClipboard } from '~/lib/clipboard'
import { hubOrigin } from '~/lib/hubSite'
import { followDestination, postSignInDestination } from '~/lib/postAuthNavigate'
import { stringParam } from '~/lib/searchParam'
import * as styles from './BrowserSignIn.css'
import * as ceremonyPage from './ceremonyPage.css'
import { useBrowserCeremony } from './useBrowserCeremony'

/**
 * Desktop sign-in view for remote-Hub mode. Runs a browser ceremony with
 * PKCE through the sidecar's loopback redirect. The Hub's own pages handle
 * the password, passkey or OAuth sign-in; the sidecar receives the session
 * cookie and this view adopts the user.
 */
export const BrowserSignIn: Component = () => {
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const auth = useAuth()

  const { phase, failure, flow, start, cancel } = useBrowserCeremony('sign-in', async () => {
    // Adopt the session the sidecar's jar already holds. The redirect rides
    // the sign-in, because a sign-in that recovers hands the navigation to
    // SignInRecoveryGate, which reads the redirect from there.
    const redirect = stringParam(searchParams.redirect)
    const result = await auth.adoptBrowserSignIn({ redirect })
    if (result.outcome === 'adopted') {
      followDestination(navigate, postSignInDestination(result.verificationRequired, redirect))
    }
    // Every other outcome: sign-in recovery or a newer operation owns the state.
  })

  return (
    <div class={ceremonyPage.container}>
      <h1 class={ceremonyPage.title}>Sign in to LeapMux</h1>
      <p class={ceremonyPage.description}>{`Hub: ${hubOrigin()}`}</p>
      <Show when={phase() === 'idle'}>
        <p class={ceremonyPage.description}>
          LeapMux signs you in through your browser.
        </p>
        <button type="button" class={styles.primaryButton} onClick={() => void start()}>
          Sign in with your browser
        </button>
      </Show>
      <Show when={phase() === 'waiting'}>
        <p class={ceremonyPage.description}>Waiting for your browser...</p>
        <div class={ceremonyPage.waitingActions}>
          <button
            type="button"
            onClick={() => {
              const f = flow()
              if (f) {
                void openExternalUrl(f.url)
              }
            }}
          >
            Open again
          </button>
          <button
            type="button"
            onClick={() => {
              const f = flow()
              if (f) {
                void copyTextToClipboard(f.url)
              }
            }}
          >
            Copy link
          </button>
          <button type="button" onClick={() => void cancel()}>
            Cancel
          </button>
        </div>
      </Show>
      <Show when={failure()}>
        <p class={ceremonyPage.error}>{failure()}</p>
      </Show>
      <button
        type="button"
        class={styles.switchMode}
        onClick={() => void platformBridge.switchMode()}
      >
        Switch mode...
      </button>
    </div>
  )
}
