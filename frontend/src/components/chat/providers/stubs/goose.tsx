import type { MessageCategory } from '../../messageClassification'
import type { PermissionMode } from '~/utils/controlResponse'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'
import { isGooseSubagentToolRequest } from './gooseShape'

// Re-export the Goose wire-shape helpers so callers can import them from the
// plugin module (the canonical provider home). The helpers themselves live in
// ./gooseShape.ts to avoid an import cycle through registerACPProvider (which
// pulls in the renderers that read the name extractor).
export { gooseSubagentToolRequestName, isGooseSubagentToolRequest } from './gooseShape'

const GOOSE_MODE_AUTO = 'auto' as PermissionMode

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
  provider: AgentProvider.GOOSE,
  defaultPermissionMode: GOOSE_MODE_AUTO,
  bypassSettings: { sets: { permissionMode: GOOSE_MODE_AUTO } },
  classifyToolCallUpdate: classifyGooseToolCallUpdate,
})
