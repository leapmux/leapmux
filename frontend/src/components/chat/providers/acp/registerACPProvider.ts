import type { Component } from 'solid-js'
import type { ActionsProps, AskQuestionState, ContentProps, Question } from '../../controls/types'
import type { AttachmentCapabilities, Provider } from '../registry'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import type { PermissionMode } from '~/utils/controlResponse'
import { ACPControlActions, ACPControlContent } from '../../controls/ACPControlRequest'
import { buildPlanMode, OPTION_ID_PERMISSION_MODE } from '../../settingsGroups'
import { registerProvider } from '../registry'
import { acpBuildControlResponse, acpExtractQuotableText, buildACPInterruptContent, classifyACPMessage } from './classification'
import { acpResultDivider } from './renderers'
import { renderACPMessage } from './rendering'

/**
 * Per-provider settings configuration for an ACP provider. The discriminator
 * picks how the provider's plan-mode/writable axis is stored: providers that
 * use the agent's top-level `permissionMode` field (Copilot, Cursor, Goose) use
 * `kind: 'permissionMode'`; providers that store it in `optionValues` under a
 * custom group key (OpenCode `primaryAgent`, Kilo) use `kind: 'optionGroup'`;
 * providers with no runtime mode at all (Reasonix) use `kind: 'modelOnly'`.
 *
 * The generic settings panel renders every reported option group on its own, so
 * this config only carries the data the registration logic needs: (per kind) the
 * default mode / writable group key + default.
 */
export type ACPSettingsPanelConfig
  = | { kind: 'permissionMode', defaultMode: PermissionMode }
    | { kind: 'optionGroup', optionGroupKey: string, defaultValue: string }
    | { kind: 'modelOnly' }

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
 * `provider`, the settings config, and the control components is derived from
 * the settings config — the `planMode` config (when `planValue` is supplied) falls
 * out of its `kind` plus its `defaultMode` / `defaultValue` / `optionGroupKey`.
 *
 * Supply EXACTLY ONE of `settingsConfig` or `defaultPermissionMode`: the latter is
 * sugar for the common `{ kind: 'permissionMode', defaultMode }` case (Copilot/
 * Cursor/Goose), mirroring how {@link registerOpenCodeProtocolProvider} hides the
 * `optionGroup` kind behind `defaultPrimaryAgent`.
 */
export interface ACPProviderOptions {
  provider: AgentProvider
  /** Explicit settings config (optionGroup / modelOnly, or an explicit permissionMode). */
  settingsConfig?: ACPSettingsPanelConfig
  /** Sugar for `settingsConfig: { kind: 'permissionMode', defaultMode }`. */
  defaultPermissionMode?: PermissionMode
  /** Control-request content component. Defaults to the shared {@link ACPControlContent}. */
  ControlContent?: Component<ContentProps>
  /** Control-request actions component. Defaults to the shared {@link ACPControlActions}. */
  ControlActions?: Component<ActionsProps>
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
  /**
   * Attachment capabilities. Defaults to full support; pass a restricted set for
   * providers that can't take every attachment kind (e.g. Reasonix is text-only).
   */
  attachments?: AttachmentCapabilities
}

/**
 * The option-group id the trigger renders as its mode segment, derived from the
 * settings config: the permission-mode field (Copilot/Cursor/Goose), the custom
 * optionGroup key (OpenCode/Kilo primaryAgent), or none (modelOnly providers have
 * no mode axis). This is the trigger analogue of planModeFromConfig's groupKey, but
 * it exists even when plan mode is not wired (Goose), so it is derived independently.
 */
function triggerModeGroupKeyForConfig(config: ACPSettingsPanelConfig): string | undefined {
  switch (config.kind) {
    case 'permissionMode':
      return OPTION_ID_PERMISSION_MODE
    case 'optionGroup':
      return config.optionGroupKey
    case 'modelOnly':
      return undefined
  }
}

/** Synthesize the plan-mode config from a settingsConfig + planValue. */
function planModeFromConfig(
  config: ACPSettingsPanelConfig,
  planValue: string,
): NonNullable<Provider['planMode']> {
  // modelOnly providers have no mode to toggle, so plan mode can't be wired. Rule it
  // out first so the remaining two kinds collapse to one shape: both store the mode in
  // `optionValues` under a group key, differing only in which key and which default.
  if (config.kind === 'modelOnly')
    throw new Error('planValue is not supported for modelOnly ACP providers')
  const { groupKey, defaultValue } = config.kind === 'permissionMode'
    ? { groupKey: OPTION_ID_PERMISSION_MODE, defaultValue: config.defaultMode }
    : { groupKey: config.optionGroupKey, defaultValue: config.defaultValue }
  return buildPlanMode(groupKey, planValue, defaultValue)
}

/**
 * Register an ACP-based provider via the shared classify/render/control wiring.
 * Each provider module reduces to a single `registerACPProvider({...})` call.
 */
export function registerACPProvider(opts: ACPProviderOptions): void {
  let sc = opts.settingsConfig
  if (!sc) {
    if (opts.defaultPermissionMode === undefined)
      throw new Error('registerACPProvider requires settingsConfig or defaultPermissionMode')
    sc = { kind: 'permissionMode', defaultMode: opts.defaultPermissionMode }
  }
  const plugin: Provider = {
    attachments: opts.attachments ?? { text: true, image: true, pdf: true, binary: true },

    classify: classifyACPMessage(opts.extraHiddenSessionUpdates
      ? { extraHiddenSessionUpdates: opts.extraHiddenSessionUpdates }
      : undefined,
    ),
    renderMessage: renderACPMessage,
    resultDivider: acpResultDivider,
    extractQuotableText: acpExtractQuotableText,
    buildInterruptContent: buildACPInterruptContent,
    buildControlResponse: acpBuildControlResponse,

    // Default to the shared ACP control UI; a provider whose payload is shaped
    // differently (Cursor) passes its own dispatching components.
    ControlContent: opts.ControlContent ?? ACPControlContent,
    ControlActions: opts.ControlActions ?? ACPControlActions,
  }

  if (opts.bypassPermissionMode !== undefined)
    plugin.bypassPermissionMode = opts.bypassPermissionMode
  if (opts.planValue !== undefined)
    plugin.planMode = planModeFromConfig(sc, opts.planValue)
  const triggerModeGroupKey = triggerModeGroupKeyForConfig(sc)
  if (triggerModeGroupKey !== undefined)
    plugin.triggerModeGroupKey = triggerModeGroupKey

  if (opts.questionHandling) {
    plugin.isAskUserQuestion = opts.questionHandling.isAskUserQuestion
    plugin.extractAskUserQuestions = opts.questionHandling.extractAskUserQuestions
    plugin.sendAskUserQuestionResponse = opts.questionHandling.sendAskUserQuestionResponse
  }

  registerProvider(opts.provider, plugin)
}
