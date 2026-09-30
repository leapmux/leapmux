import { FASTAGENT_MODE } from '~/generated/contracts/fastagent-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'

registerACPProvider({
  provider: AgentProvider.FAST_AGENT,
  defaultPermissionMode: FASTAGENT_MODE.Agent,
  // Fast Agent exposes no plan mode and no live model switch over ACP.
  // Its permission handler asks before shell, MCP, and local file tools run.
  attachments: { text: true, image: true, pdf: true, binary: false },
})
