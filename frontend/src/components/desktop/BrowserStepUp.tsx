import type { Component } from 'solid-js'
import { Show } from 'solid-js'
import { useAuth } from '~/context/AuthContext'
import { BrowserCeremonyWaiting } from './BrowserCeremonyWaiting'
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
  const ceremony = useBrowserCeremony('step-up', async (signal) => {
    if (!await auth.reloadUser())
      throw new Error('LeapMux could not read the verified session. Try again.')
    if (!signal.aborted)
      props.onElevated()
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
    <div>
      <p>LeapMux verifies you through your browser.</p>
      <Show when={ceremony.state().phase === 'idle'}>
        <button type="button" onClick={() => void ceremony.start()}>
          Verify in your browser
        </button>
      </Show>
      <Show when={ceremony.state().phase !== 'idle'}>
        <BrowserCeremonyWaiting flow={waitingFlow()} />
      </Show>
      <Show when={failure()}>
        <p class={ceremonyPage.error}>{failure()}</p>
      </Show>
    </div>
  )
}
