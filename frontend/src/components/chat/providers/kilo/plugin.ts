import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { kiloOutputFilePaths } from './extractors/outputFilePaths'
import { registerKiloProtocolProvider } from './pluginConfiguration'
import { KILO_REFUSED_TOOL_ERRORS } from './protocol'
import { kiloToolKind } from './toolKinds'

registerKiloProtocolProvider({
  outputFilePaths: kiloOutputFilePaths,
  provider: AgentProvider.KILO,
  defaultPrimaryAgent: 'code',
  refusals: KILO_REFUSED_TOOL_ERRORS,
  toolKinds: kiloToolKind,
})
