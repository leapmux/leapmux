import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { AgentProvider as AgentProviderEnum } from '~/generated/leapmux/v1/agent_pb'
import { getMruProviders } from '~/lib/mruAgentProviders'

export const DEFAULT_AGENT_PROVIDERS: AgentProvider[] = [
  AgentProviderEnum.CLAUDE_CODE,
  AgentProviderEnum.CODEX,
  AgentProviderEnum.CURSOR,
  AgentProviderEnum.GEMINI_CLI,
  AgentProviderEnum.GITHUB_COPILOT,
  AgentProviderEnum.GOOSE,
  AgentProviderEnum.KILO,
  AgentProviderEnum.OPENCODE,
]

export function getAvailableAgentProviders(availableProviders?: AgentProvider[]): AgentProvider[] {
  return availableProviders?.length ? availableProviders : DEFAULT_AGENT_PROVIDERS
}

export function sortAgentProvidersByName(providers: AgentProvider[]): AgentProvider[] {
  return providers.toSorted((a, b) => agentProviderLabel(a).localeCompare(agentProviderLabel(b)))
}

export function getDefaultAgentProvider(availableProviders?: AgentProvider[]): AgentProvider {
  const available = getAvailableAgentProviders(availableProviders)
  const stored = getMruProviders()

  for (const provider of stored) {
    if (available.includes(provider))
      return provider
  }

  return sortAgentProvidersByName(available)[0] ?? AgentProviderEnum.CLAUDE_CODE
}
