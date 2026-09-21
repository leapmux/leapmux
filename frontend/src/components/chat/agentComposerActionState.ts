import type { Accessor, Setter } from 'solid-js'
import { createRoot, createSignal, onCleanup } from 'solid-js'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'

type LoadingSignal = Pick<ReturnType<typeof createLoadingSignal>, 'loading' | 'start' | 'stop'>

export interface AgentComposerActionState {
  sending: LoadingSignal
  interrupting: LoadingSignal
  enqueueInFlight: Accessor<boolean>
  setEnqueueInFlight: Setter<boolean>
  pauseInFlight: Accessor<boolean>
  setPauseInFlight: Setter<boolean>
}

interface StoredAgentComposerActionState extends AgentComposerActionState {
  dispose: () => void
}

/**
 * Keeps composer action state with the agent that started each request.
 *
 * The shell reuses one editor panel when the focused agent tab changes. A
 * panel-wide signal therefore makes tab B inherit tab A's pending action.
 */
export function createAgentComposerActionStateStore() {
  const states = new Map<string, StoredAgentComposerActionState>()

  const forAgent = (agentId: string): AgentComposerActionState => {
    const existing = states.get(agentId)
    if (existing)
      return existing

    const created = createRoot((dispose): StoredAgentComposerActionState => {
      const [enqueueInFlight, setEnqueueInFlight] = createSignal(false)
      const [pauseInFlight, setPauseInFlight] = createSignal(false)
      return {
        sending: createLoadingSignal(),
        interrupting: createLoadingSignal(),
        enqueueInFlight,
        setEnqueueInFlight,
        pauseInFlight,
        setPauseInFlight,
        dispose,
      }
    })
    states.set(agentId, created)
    return created
  }

  const releaseAgent = (agentId: string): boolean => {
    const state = states.get(agentId)
    if (!state)
      return false
    states.delete(agentId)
    state.dispose()
    return true
  }

  onCleanup(() => {
    for (const state of states.values())
      state.dispose()
    states.clear()
  })

  return { forAgent, releaseAgent }
}

export type AgentComposerActionStateStore = ReturnType<typeof createAgentComposerActionStateStore>
