import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { registerOpenCodeProtocolProvider } from '../registerOpenCodeProtocolProvider'

registerOpenCodeProtocolProvider({
  provider: AgentProvider.OPENCODE,
  defaultPrimaryAgent: 'build',
})
