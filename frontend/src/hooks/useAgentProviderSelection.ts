import type { Accessor } from 'solid-js'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createMemo, createSignal } from 'solid-js'
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

  return {
    agentProvider,
    setAgentProvider,
    recordProviderUse,
    available,
    noProviders: () => agentProvider() === undefined,
  }
}
