import type { MessageCategory } from '../../messageClassification'
import { GOOSE_DEFAULT_MODE, GOOSE_MODE } from '~/generated/contracts/goose-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'
import { isGooseSubagentToolRequest } from './subagentToolRequest'
import { gooseToolAdapter } from './toolPresentation'

// Goose's classify hook for tool_call_update: recognize the subagent
// tool-request _meta and emit a `subagent_tool_request` tool_use category so
// the shared renderer draws a compact "Requested tool: <name>" card. Other
// tool_call_updates fall through to the shared status-based classifier.
function classifyGooseToolCallUpdate(parent: Record<string, unknown>): MessageCategory | undefined {
  if (isGooseSubagentToolRequest(parent))
    return { kind: 'tool_use', toolName: 'subagent_tool_request', toolUse: parent, content: [] }
  return undefined
}

registerACPProvider({
  toolAdapter: gooseToolAdapter,
  provider: AgentProvider.GOOSE,
  defaultPermissionMode: GOOSE_DEFAULT_MODE,
  permissionPresets: {
    smart: { sets: { permissionMode: GOOSE_MODE.SmartApprove } },
    bypass: { sets: { permissionMode: GOOSE_MODE.Auto } },
  },
  classifyToolCallUpdate: classifyGooseToolCallUpdate,
})
