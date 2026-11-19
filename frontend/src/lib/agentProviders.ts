import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { agentProviderLabel } from '~/components/common/AgentProviderIcon'

export function sortAgentProvidersByName(providers: readonly AgentProvider[]): readonly AgentProvider[] {
  return providers.toSorted((a, b) => agentProviderLabel(a).localeCompare(agentProviderLabel(b)))
}
