import type { ACPSettingsPanelConfig } from './acpShared'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { OpenCodeControlActions, OpenCodeControlContent } from '../controls/OpenCodeControlRequest'
import {
  buildACPInterruptContent,
  changeACPPermissionMode,
  classifyACPMessage,
  createACPSettingsPanel,
  createACPTriggerLabel,
  renderACPMessage,
} from './acpShared'
import { registerProvider } from './registry'

const DEFAULT_OPENCODE_MODEL = import.meta.env.LEAPMUX_OPENCODE_DEFAULT_MODEL || ''
const DEFAULT_OPENCODE_PRIMARY_AGENT = 'build'
const OPENCODE_PLAN_PRIMARY_AGENT = 'plan'
const OPENCODE_EXTRA_PRIMARY_AGENT = 'primaryAgent'

const settingsConfig: ACPSettingsPanelConfig = {
  defaultModel: DEFAULT_OPENCODE_MODEL,
  optionGroupKey: OPENCODE_EXTRA_PRIMARY_AGENT,
  defaultOptionValue: DEFAULT_OPENCODE_PRIMARY_AGENT,
  fallbackLabel: 'Primary Agent',
  testIdPrefix: 'primary-agent',
}

registerProvider(AgentProvider.OPENCODE, {
  defaultModel: DEFAULT_OPENCODE_MODEL || undefined,
  attachments: { text: true, image: true, pdf: true, binary: true },
  planMode: {
    currentMode: agent => agent.extraSettings?.[OPENCODE_EXTRA_PRIMARY_AGENT] || DEFAULT_OPENCODE_PRIMARY_AGENT,
    planValue: OPENCODE_PLAN_PRIMARY_AGENT,
    defaultValue: DEFAULT_OPENCODE_PRIMARY_AGENT,
    setMode: (mode, cb) => cb.onOptionGroupChange?.(OPENCODE_EXTRA_PRIMARY_AGENT, mode),
  },

  classify: classifyACPMessage(),
  renderMessage: renderACPMessage,
  buildInterruptContent: buildACPInterruptContent,
  changePermissionMode: changeACPPermissionMode,

  isAskUserQuestion(payload?: Record<string, unknown>): boolean {
    return payload?.type === 'question.asked'
  },

  ControlContent: OpenCodeControlContent,
  ControlActions: OpenCodeControlActions,
  SettingsPanel: createACPSettingsPanel(settingsConfig),
  settingsTriggerLabel: createACPTriggerLabel(settingsConfig),
})
