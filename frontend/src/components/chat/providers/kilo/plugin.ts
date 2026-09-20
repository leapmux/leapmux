import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerOpenCodeProtocolProvider } from '../registerOpenCodeProtocolProvider'
import { kiloToolKind } from './toolKinds'

registerOpenCodeProtocolProvider({
  provider: AgentProvider.KILO,
  defaultPrimaryAgent: 'code',
  toolKinds: kiloToolKind,
})
