import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'

/** Deliberately provider-specific helper for the semantic lint probe. */
export function isCodexProvider(provider: AgentProvider): boolean {
  return provider === AgentProvider.CODEX
}
