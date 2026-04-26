import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { registerOpenCodeProtocolProvider } from '../registerOpenCodeProtocolProvider'

registerOpenCodeProtocolProvider({
  provider: AgentProvider.KILO,
  defaultModel: import.meta.env.LEAPMUX_KILO_DEFAULT_MODEL || '',
  defaultPrimaryAgent: 'code',
})
