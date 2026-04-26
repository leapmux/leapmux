import type { ACPSettingsPanelConfig } from '../shared/acpSettings'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { extractOpenCodeQuestions, OpenCodeControlActions, OpenCodeControlContent, sendOpenCodeQuestionResponse } from '../../controls/OpenCodeControlRequest'
import { registerProvider } from '../registry'
import { acpBuildControlResponse, buildACPInterruptContent, classifyACPMessage } from '../shared/acpClassification'
import { renderACPMessage } from '../shared/acpRendering'
import { createACPSettingsPanel, createACPTriggerLabel } from '../shared/acpSettings'

const DEFAULT_KILO_MODEL = import.meta.env.LEAPMUX_KILO_DEFAULT_MODEL || ''
const DEFAULT_KILO_PRIMARY_AGENT = 'code'
const KILO_PLAN_PRIMARY_AGENT = 'plan'
const KILO_EXTRA_PRIMARY_AGENT = 'primaryAgent'

const settingsConfig: ACPSettingsPanelConfig = {
  defaultModel: DEFAULT_KILO_MODEL,
  optionGroupKey: KILO_EXTRA_PRIMARY_AGENT,
  defaultOptionValue: DEFAULT_KILO_PRIMARY_AGENT,
  fallbackLabel: 'Primary Agent',
  testIdPrefix: 'primary-agent',
}

registerProvider(AgentProvider.KILO, {
  defaultModel: DEFAULT_KILO_MODEL || undefined,
  attachments: { text: true, image: true, pdf: true, binary: true },
  planMode: {
    currentMode: agent => agent.extraSettings?.[KILO_EXTRA_PRIMARY_AGENT] || DEFAULT_KILO_PRIMARY_AGENT,
    planValue: KILO_PLAN_PRIMARY_AGENT,
    defaultValue: DEFAULT_KILO_PRIMARY_AGENT,
    setMode: (mode, cb) => cb.onOptionGroupChange?.(KILO_EXTRA_PRIMARY_AGENT, mode),
  },

  classify: classifyACPMessage(),
  renderMessage: renderACPMessage,
  buildInterruptContent: buildACPInterruptContent,
  buildControlResponse: acpBuildControlResponse,

  isAskUserQuestion(payload?: Record<string, unknown>): boolean {
    return payload?.type === 'question.asked'
  },

  extractAskUserQuestions: extractOpenCodeQuestions,

  async sendAskUserQuestionResponse(agentId, sendControlResponse, requestId, questions, askState, _payload) {
    await sendOpenCodeQuestionResponse(agentId, sendControlResponse, requestId, questions, askState)
  },

  ControlContent: OpenCodeControlContent,
  ControlActions: OpenCodeControlActions,
  SettingsPanel: createACPSettingsPanel(settingsConfig),
  settingsTriggerLabel: createACPTriggerLabel(settingsConfig),
})
