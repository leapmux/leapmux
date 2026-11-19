import type { Component } from 'solid-js'
import type { BrowserAuthFlow } from '~/lib/browserAuth'
import { createSignal, onCleanup, Show } from 'solid-js'
import { useAuth } from '~/context/AuthContext'
import { runBrowserAuth } from '~/lib/browserAuth'
import { formatErrorMessage } from '~/lib/errors'
import * as styles from './BrowserSignIn.css'

/**
 * Step-up prompt for desktop remote-Hub mode. Runs a browser ceremony to
 * stamp the step-up window on the session the sidecar's jar holds.
 */
export const BrowserStepUp: Component<{
  onElevated: () => void
}> = (props) => {
  const auth = useAuth()
  const [phase, setPhase] = createSignal<'idle' | 'waiting'>('idle')
  const [failure, setFailure] = createSignal('')
  const [flow, setFlow] = createSignal<BrowserAuthFlow | null>(null)

  const controller = new AbortController()
  onCleanup(() => controller.abort())

  const handleVerify = async (): Promise<void> => {
    setFailure('')
    setPhase('waiting')
    try {
      const f = await runBrowserAuth('step-up', controller.signal)
      setFlow(f)

      const result = await f.result
      if (!result.ok) {
        setPhase('idle')
        setFlow(null)
        if (result.failure)
          setFailure(result.failure)
        return
      }

      await auth.refreshUser()
      props.onElevated()
    }
    catch (err) {
      setPhase('idle')
      setFlow(null)
      setFailure(formatErrorMessage(err))
    }
  }

  return (
    <div>
      <p>LeapMux verifies you through your browser.</p>
      <Show when={phase() === 'idle'}>
        <button type="button" onClick={() => void handleVerify()}>
          Verify in your browser
        </button>
      </Show>
      <Show when={phase() === 'waiting'}>
        <p>Waiting for your browser...</p>
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
        </div>
      </Show>
      <Show when={failure()}>
        <p class={styles.error}>{failure()}</p>
      </Show>
    </div>
  )
}
