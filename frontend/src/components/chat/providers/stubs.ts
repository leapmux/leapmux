import type { ProviderPlugin } from './registry'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { registerProvider } from './registry'

/** Stub plugin for providers that don't have a custom classifier yet. */
const stubPlugin: ProviderPlugin = {
  classify() {
    return { kind: 'unknown' as const }
  },
}

registerProvider(AgentProvider.OPENCODE, stubPlugin)
