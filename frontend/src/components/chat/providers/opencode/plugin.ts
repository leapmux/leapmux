import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerOpenCodeProtocolProvider } from '../registerOpenCodeProtocolProvider'
import { openCodeOutputFilePaths } from './extractors/outputFilePaths'

registerOpenCodeProtocolProvider({
  outputFilePaths: openCodeOutputFilePaths,
  provider: AgentProvider.OPENCODE,
  defaultPrimaryAgent: 'build',
})
