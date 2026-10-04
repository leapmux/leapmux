import { JUNIE_MODE } from '~/generated/contracts/junie-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createACPProvider } from '../acp/registerACPProvider'
import { registerProvider } from '../registry'
import { junieOutputFilePaths } from './extractors/outputFilePaths'

const base = createACPProvider({
  outputFilePaths: junieOutputFilePaths,
  defaultPermissionMode: JUNIE_MODE.Default,
  planValue: JUNIE_MODE.Plan,
  attachments: { text: true, image: true, pdf: false, binary: false },
})

registerProvider(AgentProvider.JUNIE, base)
