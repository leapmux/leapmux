import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerOpenCodeProtocolProvider } from '../registerOpenCodeProtocolProvider'
import { openCodeOutputFilePaths } from './extractors/outputFilePaths'
import { OPENCODE_REFUSED_TOOL_ERRORS } from './protocol'

registerOpenCodeProtocolProvider({
  outputFilePaths: openCodeOutputFilePaths,
  provider: AgentProvider.OPENCODE,
  defaultPrimaryAgent: 'build',
  refusals: OPENCODE_REFUSED_TOOL_ERRORS,
})
