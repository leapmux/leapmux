import type { Component } from 'solid-js'
import type { ActionsProps, AskQuestionState, ContentProps, Question } from '../../controls/types'
import type { Provider } from '../registry'
import type { ACPSettingsPanelConfig } from './settings'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import type { PermissionMode } from '~/utils/controlResponse'
import { registerProvider } from '../registry'
import { acpBuildControlResponse, acpExtractQuotableText, buildACPInterruptContent, changeACPPermissionMode, classifyACPMessage } from './classification'
import { renderACPMessage } from './rendering'
import { createACPSettingsPanel, createACPTriggerLabel } from './settings'

/**
 * Per-provider question-handling hooks. Providers that delegate `AskUserQuestion`
 * to the shared ACP path leave this unset; OpenCode/Kilo/Cursor each plug in
 * their own payload sniffer + extractor + responder.
 */
export interface ACPQuestionHandling {
  isAskUserQuestion: NonNullable<Provider['isAskUserQuestion']>
  extractAskUserQuestions: NonNullable<Provider['extractAskUserQuestions']>
  sendAskUserQuestionResponse: (
    agentId: string,
    sendControlResponse: (agentId: string, bytes: Uint8Array) => Promise<void>,
    requestId: string,
    questions: Question[],
    askState: AskQuestionState,
  ) => Promise<void>
}

/**
 * Options accepted by {@link registerACPProvider}. Every value beyond
 * `provider`, `settingsConfig`, and the control components is derived from
 * `settingsConfig` — `defaultModel`, `defaultPermissionMode`, the
 * `changePermissionMode` dispatcher, and the read/write halves of `planMode`
 * (when `planValue` is supplied) all fall out of `settingsConfig.kind` plus
 * its `defaultMode` / `defaultValue` / `optionGroupKey`.
 */
export interface ACPProviderOptions {
  provider: AgentProvider
  settingsConfig: ACPSettingsPanelConfig
  ControlContent: Component<ContentProps>
  ControlActions: Component<ActionsProps>
  /**
   * Mode value that represents "plan" for this provider's plan-mode toggle.
   * Omit to disable plan-mode wiring (e.g. Goose has no plan mode).
   */
  planValue?: string
  /** Identifier of the bypass mode for the "& Bypass Permissions" button. */
  bypassPermissionMode?: PermissionMode
  /** Question-handling hooks for providers that override the default ACP path. */
  questionHandling?: ACPQuestionHandling
  /** Extra `session/update` types that should be hidden from the chat. */
  extraHiddenSessionUpdates?: Set<string>
}

/** Synthesize the default plan-mode read/write halves from a settingsConfig + planValue. */
function planModeFromConfig(
  config: ACPSettingsPanelConfig,
  planValue: string,
): NonNullable<Provider['planMode']> {
  if (config.kind === 'permissionMode') {
    const fallback = config.defaultMode
    return {
      currentMode: agent => agent.permissionMode || fallback,
      planValue,
      defaultValue: fallback,
      setMode: (mode, onChange) => onChange({ kind: 'permissionMode', value: mode as PermissionMode }),
    }
  }
  const { optionGroupKey, defaultValue } = config
  return {
    currentMode: agent => agent.extraSettings?.[optionGroupKey] || defaultValue,
    planValue,
    defaultValue,
    setMode: (mode, onChange) => onChange({ kind: 'optionGroup', key: optionGroupKey, value: mode }),
  }
}

/**
 * Register an ACP-based provider via the shared classify/render/control wiring.
 * Each provider module reduces to a single `registerACPProvider({...})` call.
 */
export function registerACPProvider(opts: ACPProviderOptions): void {
  const sc = opts.settingsConfig
  const plugin: Provider = {
    defaultModel: sc.defaultModel || undefined,
    attachments: { text: true, image: true, pdf: true, binary: true },

    classify: classifyACPMessage(opts.extraHiddenSessionUpdates
      ? { extraHiddenSessionUpdates: opts.extraHiddenSessionUpdates }
      : undefined,
    ),
    renderMessage: renderACPMessage,
    extractQuotableText: acpExtractQuotableText,
    buildInterruptContent: buildACPInterruptContent,
    buildControlResponse: acpBuildControlResponse,

    ControlContent: opts.ControlContent,
    ControlActions: opts.ControlActions,
    SettingsPanel: createACPSettingsPanel(sc),
    settingsTriggerLabel: createACPTriggerLabel(sc),
  }

  if (sc.kind === 'permissionMode') {
    plugin.defaultPermissionMode = sc.defaultMode
    plugin.changePermissionMode = changeACPPermissionMode
  }
  if (opts.bypassPermissionMode !== undefined)
    plugin.bypassPermissionMode = opts.bypassPermissionMode
  if (opts.planValue !== undefined)
    plugin.planMode = planModeFromConfig(sc, opts.planValue)

  if (opts.questionHandling) {
    plugin.isAskUserQuestion = opts.questionHandling.isAskUserQuestion
    plugin.extractAskUserQuestions = opts.questionHandling.extractAskUserQuestions
    plugin.sendAskUserQuestionResponse = opts.questionHandling.sendAskUserQuestionResponse
  }

  registerProvider(opts.provider, plugin)
}
