import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { ALL_PROVIDERS } from '~/generated/contracts/providers'

// Providers shown in the agent picker before the worker's
// ListAvailableProviders probe returns. Generated from contracts/providers.json
// (the Go twin is contracts.AllProviders), so adding a provider to the
// contract regenerates this list too; once the probe completes, the picker
// switches to the actually-installed subset.
export const DEFAULT_AGENT_PROVIDERS: readonly AgentProvider[] = ALL_PROVIDERS

export function getAvailableAgentProviders(availableProviders?: readonly AgentProvider[]): readonly AgentProvider[] {
  return availableProviders ?? DEFAULT_AGENT_PROVIDERS
}

export function sortAgentProvidersByName(providers: readonly AgentProvider[]): readonly AgentProvider[] {
  return providers.toSorted((a, b) => agentProviderLabel(a).localeCompare(agentProviderLabel(b)))
}
