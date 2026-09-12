import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerOpenCodeProtocolProvider } from '../registerOpenCodeProtocolProvider'

registerOpenCodeProtocolProvider({
  provider: AgentProvider.KILO,
  defaultPrimaryAgent: 'code',
})
