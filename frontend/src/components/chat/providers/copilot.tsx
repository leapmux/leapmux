import type { ACPSettingsPanelConfig } from './acpShared'
import type { PermissionMode } from '~/utils/controlResponse'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { ACPControlActions, ACPControlContent } from '../controls/ACPControlRequest'
import { PERMISSION_MODE_KEY } from '../settingsShared'
import {
  buildACPInterruptContent,
  changeACPPermissionMode,
  classifyACPMessage,
  createACPSettingsPanel,
  createACPTriggerLabel,
  renderACPMessage,
} from './acpShared'
import { registerProvider } from './registry'

const DEFAULT_COPILOT_MODEL = import.meta.env.LEAPMUX_COPILOT_DEFAULT_MODEL || ''
const COPILOT_MODE_AGENT = 'https://agentclientprotocol.com/protocol/session-modes#agent'
const COPILOT_MODE_PLAN = 'https://agentclientprotocol.com/protocol/session-modes#plan'
const COPILOT_MODE_AUTOPILOT = 'https://agentclientprotocol.com/protocol/session-modes#autopilot'

const settingsConfig: ACPSettingsPanelConfig = {
  defaultModel: DEFAULT_COPILOT_MODEL,
  optionGroupKey: PERMISSION_MODE_KEY,
  defaultOptionValue: COPILOT_MODE_AGENT,
  fallbackLabel: 'Mode',
  testIdPrefix: 'permission-mode',
}

registerProvider(AgentProvider.COPILOT_CLI, {
  defaultModel: DEFAULT_COPILOT_MODEL || undefined,
  defaultPermissionMode: COPILOT_MODE_AGENT,
  attachments: { text: true, image: true, pdf: true, binary: true },
  bypassPermissionMode: COPILOT_MODE_AUTOPILOT,
  planMode: {
    currentMode: agent => agent.permissionMode || COPILOT_MODE_AGENT,
    planValue: COPILOT_MODE_PLAN,
    defaultValue: COPILOT_MODE_AGENT,
    setMode: (mode, cb) => cb.onPermissionModeChange?.(mode as PermissionMode),
  },

  classify: classifyACPMessage({ extraHiddenSessionUpdates: new Set(['config_option_update']) }),
  renderMessage: renderACPMessage,
  buildInterruptContent: buildACPInterruptContent,
  changePermissionMode: changeACPPermissionMode,

  ControlContent: ACPControlContent,
  ControlActions: ACPControlActions,
  SettingsPanel: createACPSettingsPanel(settingsConfig),
  settingsTriggerLabel: createACPTriggerLabel(settingsConfig),
})
