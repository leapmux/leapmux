import { DIRAC_CONFIG, DIRAC_MODE } from '~/generated/contracts/dirac-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'
import { diracToolCallAdapter } from './extractors/toolCall'

registerACPProvider({
  provider: AgentProvider.DIRAC,
  toolCallAdapter: diracToolCallAdapter,
  defaultPermissionMode: DIRAC_MODE.Act,
  planValue: DIRAC_MODE.Plan,
  effortGroupKey: DIRAC_CONFIG.ReasoningEffort,
})
