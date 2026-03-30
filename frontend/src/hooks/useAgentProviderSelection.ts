import type { Accessor } from 'solid-js'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { createEffect, createMemo, createSignal } from 'solid-js'
import { getAvailableAgentProviders, getDefaultAgentProvider, sortAgentProvidersByName } from '~/lib/agentProviders'
import { touchMruProvider } from '~/lib/mruAgentProviders'

interface UseAgentProviderSelectionResult {
  agentProvider: Accessor<AgentProvider>
  noProviders: Accessor<boolean>
  handleProviderChange: (provider: AgentProvider) => void
  commitSelection: () => void
}

/**
 * Manages agent provider selection with MRU-based defaults.
 *
 * Tracks whether the user has explicitly chosen a provider. When the
 * available-providers list changes, the selection resets to the MRU default
 * unless the user has touched it and their choice is still available.
 *
 * Call `commitSelection()` on successful submit to persist the choice to MRU.
 */
export function useAgentProviderSelection(availableProviders: Accessor<AgentProvider[] | undefined>): UseAgentProviderSelectionResult {
  const providers = createMemo(() => sortAgentProvidersByName(getAvailableAgentProviders(availableProviders())))
  const defaultProvider = createMemo(() => getDefaultAgentProvider(availableProviders()))
  const [agentProvider, setAgentProvider] = createSignal<AgentProvider>(defaultProvider())
  let touched = false

  createEffect(() => {
    const next = defaultProvider()
    if (!touched || !availableProviders()?.includes(agentProvider()))
      setAgentProvider(next)
  })

  return {
    agentProvider,
    noProviders: () => providers().length === 0,
    handleProviderChange(provider: AgentProvider) {
      touched = true
      setAgentProvider(provider)
    },
    commitSelection() {
      touchMruProvider(agentProvider())
    },
  }
}
