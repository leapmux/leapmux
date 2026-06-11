import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'

const COPILOT_MODE_AGENT = 'https://agentclientprotocol.com/protocol/session-modes#agent'
const COPILOT_MODE_PLAN = 'https://agentclientprotocol.com/protocol/session-modes#plan'
const COPILOT_MODE_AUTOPILOT = 'https://agentclientprotocol.com/protocol/session-modes#autopilot'

registerACPProvider({
  provider: AgentProvider.GITHUB_COPILOT,
  defaultPermissionMode: COPILOT_MODE_AGENT,
  planValue: COPILOT_MODE_PLAN,
  bypassPermissionMode: COPILOT_MODE_AUTOPILOT,
})
