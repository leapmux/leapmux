import { JUNIE_MODE } from '~/generated/contracts/junie-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'
import { junieToolCallAdapter } from './extractors/toolCall'

registerACPProvider({
  provider: AgentProvider.JUNIE,
  toolCallAdapter: junieToolCallAdapter,
  defaultPermissionMode: JUNIE_MODE.Default,
  planValue: JUNIE_MODE.Plan,
})
