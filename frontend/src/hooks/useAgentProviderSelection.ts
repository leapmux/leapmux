import type { Accessor } from 'solid-js'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createEffect, createMemo, createSignal } from 'solid-js'
import { useAvailableProviders } from '~/hooks/useAvailableProviders'
import { useMruProviders } from '~/hooks/useMruProviders'

const emptyProviders: readonly AgentProvider[] = []

/** Select a provider from the worker's latest list. */
export function useAgentProviderSelection(
  availableProviders: Accessor<readonly AgentProvider[] | undefined>,
) {
  const available = createMemo(() => availableProviders() ?? emptyProviders)
  const { mruProviders, recordProviderUse } = useMruProviders(available, 1)
  const [preferredProvider, setAgentProvider] = createSignal<AgentProvider>()
  const agentProvider = createMemo(() => {
    const preferred = preferredProvider()
    return preferred !== undefined && available().includes(preferred)
      ? preferred
      : mruProviders()[0]
  })

  createEffect(() => setAgentProvider(agentProvider()))

  return {
    agentProvider,
    setAgentProvider,
    recordProviderUse,
    available,
    noProviders: () => agentProvider() === undefined,
  }
}

/** Use one worker's discovery result for both the picker and form submission. */
export function useWorkerAgentProviderSelection(workerId: Accessor<string>, enabled?: Accessor<boolean>) {
  const availability = useAvailableProviders(workerId, enabled)
  const selection = useAgentProviderSelection(availability.providers)
  return {
    ...selection,
    picker: {
      value: selection.agentProvider,
      onChange: selection.setAgentProvider,
      get availableProviders() { return availability.providers() },
      get loading() { return availability.loading() },
      onRefresh: availability.refresh,
    },
  }
}
