import { COPILOT_PERMISSION_GROUP, COPILOT_PERMISSION_VALUE } from '~/generated/contracts/copilot-permissions'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'

const COPILOT_MODE_AGENT = 'https://agentclientprotocol.com/protocol/session-modes#agent'
const COPILOT_MODE_PLAN = 'https://agentclientprotocol.com/protocol/session-modes#plan'

registerACPProvider({
  provider: AgentProvider.GITHUB_COPILOT,
  defaultPermissionMode: COPILOT_MODE_AGENT,
  planValue: COPILOT_MODE_PLAN,
  permissionPresets: {
    smart: { sets: { [COPILOT_PERMISSION_GROUP.AssistedApproval]: COPILOT_PERMISSION_VALUE.On } },
    bypass: { sets: { [COPILOT_PERMISSION_GROUP.AllowAll]: COPILOT_PERMISSION_VALUE.On } },
  },
})
