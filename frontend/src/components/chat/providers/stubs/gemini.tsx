import type { ACPSettingsPanelConfig } from '../shared/acpSettings'
import type { PermissionMode } from '~/utils/controlResponse'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { ACPControlActions, ACPControlContent } from '../../controls/ACPControlRequest'
import { PERMISSION_MODE_KEY } from '../../settingsShared'
import { registerProvider } from '../registry'
import { acpBuildControlResponse, buildACPInterruptContent, changeACPPermissionMode, classifyACPMessage } from '../shared/acpClassification'
import { renderACPMessage } from '../shared/acpRendering'
import { createACPSettingsPanel, createACPTriggerLabel } from '../shared/acpSettings'

const DEFAULT_GEMINI_MODEL = import.meta.env.LEAPMUX_GEMINI_DEFAULT_MODEL || 'auto'
const DEFAULT_GEMINI_MODE = 'default'
const GEMINI_PLAN_MODE = 'plan'

const settingsConfig: ACPSettingsPanelConfig = {
  defaultModel: DEFAULT_GEMINI_MODEL,
  optionGroupKey: PERMISSION_MODE_KEY,
  defaultOptionValue: DEFAULT_GEMINI_MODE,
  fallbackLabel: 'Permission Mode',
  testIdPrefix: 'permission-mode',
}

registerProvider(AgentProvider.GEMINI_CLI, {
  defaultModel: DEFAULT_GEMINI_MODEL,
  defaultPermissionMode: DEFAULT_GEMINI_MODE as PermissionMode,
  attachments: { text: true, image: true, pdf: true, binary: true },
  bypassPermissionMode: 'yolo',
  planMode: {
    currentMode: agent => agent.permissionMode || DEFAULT_GEMINI_MODE,
    planValue: GEMINI_PLAN_MODE,
    defaultValue: DEFAULT_GEMINI_MODE,
    setMode: (mode, cb) => cb.onPermissionModeChange?.(mode as PermissionMode),
  },

  classify: classifyACPMessage(),
  renderMessage: renderACPMessage,
  buildInterruptContent: buildACPInterruptContent,
  buildControlResponse: acpBuildControlResponse,
  changePermissionMode: changeACPPermissionMode,

  ControlContent: ACPControlContent,
  ControlActions: ACPControlActions,
  SettingsPanel: createACPSettingsPanel(settingsConfig),
  settingsTriggerLabel: createACPTriggerLabel(settingsConfig),
})
