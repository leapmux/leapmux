import { FASTAGENT_MODE } from '~/generated/contracts/fastagent-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'

registerACPProvider({
  provider: AgentProvider.FAST_AGENT,
  defaultPermissionMode: FASTAGENT_MODE.Agent,
  // fast-agent exposes no plan mode and no per-session model switch over ACP.
  // Its local filesystem tools bypass the permission system, so the permission
  // axis carries only the shell and MCP tools that do gate.
})
