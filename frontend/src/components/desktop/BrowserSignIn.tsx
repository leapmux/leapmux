import type { Component } from 'solid-js'
import type { BrowserAuthFlow } from '~/lib/browserAuth'
import { useNavigate } from '@solidjs/router'
import { createSignal, onCleanup, Show } from 'solid-js'
import { useAuth } from '~/context/AuthContext'
import { runBrowserAuth } from '~/lib/browserAuth'
import { formatErrorMessage } from '~/lib/errors'
import { followDestination, postSignInDestination } from '~/lib/postAuthNavigate'
import * as styles from './BrowserSignIn.css'

/**
 * Desktop sign-in view for remote-Hub mode. Runs a browser ceremony with
 * PKCE through the sidecar's loopback redirect. The Hub's own pages handle
 * the password, passkey or OAuth sign-in; the sidecar receives the session
 * cookie and this view adopts the user.
 */
export const BrowserSignIn: Component = () => {
  const navigate = useNavigate()
  const auth = useAuth()
  const [phase, setPhase] = createSignal<'idle' | 'waiting'>('idle')
  const [failure, setFailure] = createSignal('')
  const [flow, setFlow] = createSignal<BrowserAuthFlow | null>(null)

  const controller = new AbortController()
  onCleanup(() => controller.abort())

  const handleSignIn = async (): Promise<void> => {
    setFailure('')
    setPhase('waiting')
    try {
      const f = await runBrowserAuth('sign-in', controller.signal)
      setFlow(f)

      const result = await f.result
      if (!result.ok) {
        setPhase('idle')
        setFlow(null)
        if (result.failure)
          setFailure(result.failure)
        return
      }

      // Adopt the session the sidecar's jar already holds.
      const result2 = await auth.adoptBrowserSignIn()
      if (result2.outcome === 'adopted') {
        followDestination(navigate, postSignInDestination(result2.verificationRequired, undefined))
      }
      // Every other outcome: sign-in recovery or a newer operation owns the state.
    }
    catch (err) {
      setPhase('idle')
      setFlow(null)
      setFailure(formatErrorMessage(err))
    }
  }

  const handleCancel = async (): Promise<void> => {
    const f = flow()
    if (f) {
      await f.cancel()
    }
    // The result event will settle and reset the phase.
  }

  return (
    <div class={styles.container}>
      <h1 class={styles.title}>Sign in to LeapMux</h1>
      <Show when={phase() === 'idle'}>
        <p class={styles.description}>
          LeapMux signs you in through your browser.
        </p>
        <button type="button" class={styles.primaryButton} onClick={() => void handleSignIn()}>
          Sign in with your browser
        </button>
      </Show>
      <Show when={phase() === 'waiting'}>
        <p class={styles.description}>Waiting for your browser...</p>
        <div class={styles.waitingActions}>
          <button
            type="button"
            onClick={() => {
              const f = flow()
              if (f) {
                void import('~/api/platformBridge').then(({ openExternalUrl }) => openExternalUrl(f.url))
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
                void navigator.clipboard.writeText(f.url)
              }
            }}
          >
            Copy link
          </button>
          <button type="button" onClick={() => void handleCancel()}>
            Cancel
          </button>
        </div>
      </Show>
      <Show when={failure()}>
        <p class={styles.error}>{failure()}</p>
      </Show>
      <button
        type="button"
        class={styles.switchMode}
        onClick={() => {
          void import('~/api/platformBridge').then(({ platformBridge }) => platformBridge.switchMode())
        }}
      >
        Switch mode...
      </button>
    </div>
  )
}
