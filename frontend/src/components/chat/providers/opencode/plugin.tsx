import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { registerOpenCodeProtocolProvider } from '../registerOpenCodeProtocolProvider'

registerOpenCodeProtocolProvider({
  provider: AgentProvider.OPENCODE,
  defaultModel: import.meta.env.LEAPMUX_OPENCODE_DEFAULT_MODEL || '',
  defaultPrimaryAgent: 'build',
})
