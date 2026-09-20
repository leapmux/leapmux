import { GOOSE_DEFAULT_MODE, GOOSE_MODE } from '~/generated/contracts/goose-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'
import { classifyGooseToolCallUpdate } from './classification'
import { gooseToolCallAdapter } from './extractors/toolCall'

registerACPProvider({
  toolCallAdapter: gooseToolCallAdapter,
  provider: AgentProvider.GOOSE,
  defaultPermissionMode: GOOSE_DEFAULT_MODE,
  permissionPresets: {
    smart: { sets: { permissionMode: GOOSE_MODE.SmartApprove } },
    bypass: { sets: { permissionMode: GOOSE_MODE.Auto } },
  },
  classifyToolCallUpdate: classifyGooseToolCallUpdate,
})
