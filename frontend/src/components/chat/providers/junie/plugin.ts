import { JUNIE_MODE } from '~/generated/contracts/junie-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'
import { junieOutputFilePaths } from './extractors/outputFilePaths'

registerACPProvider({
  provider: AgentProvider.JUNIE,
  outputFilePaths: junieOutputFilePaths,
  defaultPermissionMode: JUNIE_MODE.Default,
  planValue: JUNIE_MODE.Plan,
  attachments: { text: true, image: true, pdf: false, binary: false },
})
