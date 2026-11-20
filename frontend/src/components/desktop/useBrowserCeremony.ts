import type { BrowserAuthKind } from '~/api/platformBridge'
import type { BrowserAuthFlow } from '~/lib/browserAuth'
import { createSignal, onCleanup } from 'solid-js'
import { runBrowserAuth } from '~/lib/browserAuth'
import { formatErrorMessage } from '~/lib/errors'

export type BrowserCeremonyState
  = | { phase: 'idle', failure: string }
    | { phase: 'starting' | 'completing' }
    | { phase: 'waiting', flow: BrowserAuthFlow }

export interface BrowserCeremony {
  state: () => BrowserCeremonyState
  start: () => Promise<void>
  cancel: () => Promise<void>
}

/**
 * Drives one browser ceremony for the desktop remote-Hub views.
 *
 * The sign-in and the step-up views share this state machine and differ only
 * in what a settled success does, which `onSuccess` supplies. `start` runs the
 * ceremony and resolves once it settles: a failure returns the view to the
 * first state and shows the reason, and a success leaves the waiting state in
 * place, because `onSuccess` navigates or closes the view. `cancel` asks the
 * ceremony to stop but settles nothing itself; the one result event per
 * ceremony resets the state. An unmount aborts the run, which the sidecar
 * treats as a cancel.
 */
export function useBrowserCeremony(
  kind: BrowserAuthKind,
  onSuccess: (signal: AbortSignal) => void | Promise<void>,
): BrowserCeremony {
  const [state, setState] = createSignal<BrowserCeremonyState>({ phase: 'idle', failure: '' })

  const controller = new AbortController()
  const { signal } = controller
  onCleanup(() => controller.abort())

  const start = async (): Promise<void> => {
    if (signal.aborted || state().phase !== 'idle')
      return
    setState({ phase: 'starting' })
    try {
      const f = await runBrowserAuth(kind, signal)
      if (!signal.aborted)
        setState({ phase: 'waiting', flow: f })

      const result = await f.result
      if (!result.ok) {
        if (signal.aborted)
          return
        setState({ phase: 'idle', failure: result.failure })
        return
      }

      // The sidecar may accept a code after this route requested cancellation.
      // Persistent authentication belongs to the surviving provider, while
      // onSuccess uses signal to suppress route-owned navigation and callbacks.
      if (!signal.aborted)
        setState({ phase: 'completing' })
      await onSuccess(signal)
    }
    catch (err) {
      if (signal.aborted)
        return
      setState({
        phase: 'idle',
        failure: formatErrorMessage(err),
      })
    }
  }

  const cancel = async (): Promise<void> => {
    const current = state()
    if (current.phase === 'waiting')
      await current.flow.cancel()
    // The result event will settle and reset the phase.
  }

  return { state, start, cancel }
}
