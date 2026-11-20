import type { BrowserAuthKind } from '~/api/platformBridge'
import type { BrowserAuthFlow } from '~/lib/browserAuth'
import { createSignal, onCleanup } from 'solid-js'
import { runBrowserAuth } from '~/lib/browserAuth'
import { formatErrorMessage } from '~/lib/errors'

export type BrowserCeremonyPhase = 'idle' | 'waiting'

export interface BrowserCeremony {
  phase: () => BrowserCeremonyPhase
  failure: () => string
  flow: () => BrowserAuthFlow | null
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
  onSuccess: () => void | Promise<void>,
): BrowserCeremony {
  const [phase, setPhase] = createSignal<BrowserCeremonyPhase>('idle')
  const [failure, setFailure] = createSignal('')
  const [flow, setFlow] = createSignal<BrowserAuthFlow | null>(null)

  const controller = new AbortController()
  onCleanup(() => controller.abort())

  const start = async (): Promise<void> => {
    setFailure('')
    setPhase('waiting')
    try {
      const f = await runBrowserAuth(kind, controller.signal)
      setFlow(f)

      const result = await f.result
      if (!result.ok) {
        setPhase('idle')
        setFlow(null)
        if (result.failure)
          setFailure(result.failure)
        return
      }

      await onSuccess()
    }
    catch (err) {
      setPhase('idle')
      setFlow(null)
      setFailure(formatErrorMessage(err))
    }
  }

  const cancel = async (): Promise<void> => {
    const f = flow()
    if (f) {
      await f.cancel()
    }
    // The result event will settle and reset the phase.
  }

  return { phase, failure, flow, start, cancel }
}
