import type { ACPSettingsPanelConfig } from '../acp/settings'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { ACPControlActions, ACPControlContent } from '../../controls/ACPControlRequest'
import { registerACPProvider } from '../acp/registerACPProvider'

const DEFAULT_COPILOT_MODEL = import.meta.env.LEAPMUX_COPILOT_DEFAULT_MODEL || ''
const COPILOT_MODE_AGENT = 'https://agentclientprotocol.com/protocol/session-modes#agent'
const COPILOT_MODE_PLAN = 'https://agentclientprotocol.com/protocol/session-modes#plan'
const COPILOT_MODE_AUTOPILOT = 'https://agentclientprotocol.com/protocol/session-modes#autopilot'

const settingsConfig: ACPSettingsPanelConfig = {
  kind: 'permissionMode',
  defaultModel: DEFAULT_COPILOT_MODEL,
  defaultMode: COPILOT_MODE_AGENT,
  fallbackLabel: 'Mode',
  testIdPrefix: 'permission-mode',
}

registerACPProvider({
  provider: AgentProvider.GITHUB_COPILOT,
  settingsConfig,
  ControlContent: ACPControlContent,
  ControlActions: ACPControlActions,
  planValue: COPILOT_MODE_PLAN,
  bypassPermissionMode: COPILOT_MODE_AUTOPILOT,
  extraHiddenSessionUpdates: new Set(['config_option_update']),
})
