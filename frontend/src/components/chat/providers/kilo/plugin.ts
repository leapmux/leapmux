import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerOpenCodeProtocolProvider } from '../registerOpenCodeProtocolProvider'
import { kiloOutputFilePaths } from './extractors/outputFilePaths'
import { KILO_REFUSED_TOOL_ERRORS } from './protocol'
import { kiloToolKind } from './toolKinds'

registerOpenCodeProtocolProvider({
  outputFilePaths: kiloOutputFilePaths,
  provider: AgentProvider.KILO,
  defaultPrimaryAgent: 'code',
  refusals: KILO_REFUSED_TOOL_ERRORS,
  toolKinds: kiloToolKind,
})
