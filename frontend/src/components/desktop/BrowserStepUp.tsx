import type { Component } from 'solid-js'
import { Show } from 'solid-js'
import { openExternalUrl } from '~/api/platformBridge'
import { useAuth } from '~/context/AuthContext'
import { copyTextToClipboard } from '~/lib/clipboard'
import * as ceremonyPage from './ceremonyPage.css'
import { useBrowserCeremony } from './useBrowserCeremony'

/**
 * Step-up prompt for desktop remote-Hub mode. Runs a browser ceremony to
 * stamp the step-up window on the session the sidecar's jar holds.
 */
export const BrowserStepUp: Component<{
  onElevated: () => void
}> = (props) => {
  const auth = useAuth()
  const { phase, failure, flow, start } = useBrowserCeremony('step-up', async () => {
    await auth.refreshUser()
    props.onElevated()
  })

  return (
    <div>
      <p>LeapMux verifies you through your browser.</p>
      <Show when={phase() === 'idle'}>
        <button type="button" onClick={() => void start()}>
          Verify in your browser
        </button>
      </Show>
      <Show when={phase() === 'waiting'}>
        <p>Waiting for your browser...</p>
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
        </div>
      </Show>
      <Show when={failure()}>
        <p class={ceremonyPage.error}>{failure()}</p>
      </Show>
    </div>
  )
}
