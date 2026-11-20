import type { Location } from '@solidjs/router'
import type { Component } from 'solid-js'
import { useLocation, useNavigate } from '@solidjs/router'
import { Show } from 'solid-js'
import { platformBridge } from '~/api/platformBridge'
import { useAuth } from '~/context/AuthContext'
import { hubOrigin } from '~/lib/hubSite'
import { followDestination, normalizeRoutePath, postSignInDestination } from '~/lib/postAuthNavigate'
import { stringParam } from '~/lib/searchParam'
import { BrowserCeremonyWaiting } from './BrowserCeremonyWaiting'
import * as styles from './BrowserSignIn.css'
import * as ceremonyPage from './ceremonyPage.css'
import { useBrowserCeremony } from './useBrowserCeremony'

/** Recovery owns navigation after the sign-in view is gone. */
function resolveRecoveryRedirect(location: Location): string | null | undefined {
  if (normalizeRoutePath(location.pathname).toLowerCase() === '/login')
    return stringParam(location.query.redirect)
  return null
}

/**
 * Desktop sign-in view for remote-Hub mode. Runs a browser ceremony with
 * PKCE through the sidecar's loopback redirect. The Hub's own pages handle
 * the password, passkey or OAuth sign-in; the sidecar receives the session
 * cookie and this view adopts the user.
 */
export const BrowserSignIn: Component<{
  /** The pathname owner survives the guard hiding this view after adoption. */
  isCurrentArrival: () => boolean
}> = (props) => {
  const navigate = useNavigate()
  const location = useLocation()
  const auth = useAuth()

  const ceremony = useBrowserCeremony('sign-in', async () => {
    // Recovery resolves the redirect after the account read completes, even
    // when the gate has already hidden this view.
    const result = await auth.adoptBrowserSignIn({ redirect: resolveRecoveryRedirect })
    if (result.outcome === 'adopted' && props.isCurrentArrival()) {
      const redirect = stringParam(location.query.redirect)
      followDestination(navigate, postSignInDestination(result.verificationRequired, redirect))
    }
    // Every other outcome: sign-in recovery or a newer operation owns the state.
  })
  const waitingFlow = () => {
    const state = ceremony.state()
    return state.phase === 'waiting'
      ? state.flow
      : null
  }
  const failure = () => {
    const state = ceremony.state()
    return state.phase === 'idle'
      ? state.failure
      : ''
  }

  return (
    <div class={ceremonyPage.container}>
      <h1 class={ceremonyPage.title}>Sign in to LeapMux</h1>
      <p class={ceremonyPage.description}>{`Hub: ${hubOrigin()}`}</p>
      <Show when={ceremony.state().phase === 'idle'}>
        <p class={ceremonyPage.description}>
          LeapMux signs you in through your browser.
        </p>
        <button type="button" class={styles.primaryButton} onClick={() => void ceremony.start()}>
          Sign in with your browser
        </button>
      </Show>
      <Show when={ceremony.state().phase !== 'idle'}>
        <BrowserCeremonyWaiting
          flow={waitingFlow()}
          descriptionClass={ceremonyPage.description}
          onCancel={ceremony.cancel}
        />
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
