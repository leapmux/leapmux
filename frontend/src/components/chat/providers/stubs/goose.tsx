import type { ACPSettingsPanelConfig } from '../shared/acpSettings'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { ACPControlActions, ACPControlContent } from '../../controls/ACPControlRequest'
import { PERMISSION_MODE_KEY } from '../../settingsShared'
import { registerProvider } from '../registry'
import { acpBuildControlResponse, buildACPInterruptContent, changeACPPermissionMode, classifyACPMessage } from '../shared/acpClassification'
import { renderACPMessage } from '../shared/acpRendering'
import { createACPSettingsPanel, createACPTriggerLabel } from '../shared/acpSettings'

const DEFAULT_GOOSE_MODEL = import.meta.env.LEAPMUX_GOOSE_DEFAULT_MODEL || ''
const GOOSE_MODE_AUTO = 'auto'

const settingsConfig: ACPSettingsPanelConfig = {
  defaultModel: DEFAULT_GOOSE_MODEL,
  optionGroupKey: PERMISSION_MODE_KEY,
  defaultOptionValue: GOOSE_MODE_AUTO,
  fallbackLabel: 'Mode',
  testIdPrefix: 'permission-mode',
}

registerProvider(AgentProvider.GOOSE, {
  defaultModel: DEFAULT_GOOSE_MODEL || undefined,
  defaultPermissionMode: GOOSE_MODE_AUTO,
  attachments: { text: true, image: true, pdf: true, binary: true },
  bypassPermissionMode: GOOSE_MODE_AUTO,

  classify: classifyACPMessage({ extraHiddenSessionUpdates: new Set(['config_option_update']) }),
  renderMessage: renderACPMessage,
  buildInterruptContent: buildACPInterruptContent,
  buildControlResponse: acpBuildControlResponse,
  changePermissionMode: changeACPPermissionMode,

  ControlContent: ACPControlContent,
  ControlActions: ACPControlActions,
  SettingsPanel: createACPSettingsPanel(settingsConfig),
  settingsTriggerLabel: createACPTriggerLabel(settingsConfig),
})
