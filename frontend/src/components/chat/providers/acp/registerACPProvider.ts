import type { MessageCategory } from '../../messageClassifier'
import type { ProviderPermissionPresets } from '../../providerSettings'
import type { AttachmentCapabilities, ProviderAskUserQuestion, ProviderConfigurationCapability, ProviderControlCapability, ProviderPlugin } from '../capabilities'
import type { ACPPermissionRejectReason } from './controlResponse'
import type { ACPToolCallAdapter } from './extractors/toolCall'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { PermissionMode } from '~/utils/controlResponse'
import { withElicitationResponse } from '../../controls/elicitationResponse'
import { sendSelectedOptionResponse } from '../../controls/types'
import { buildPlanMode, OPTION_ID_PERMISSION_MODE } from '../../settingsGroups'
import { registerProvider } from '../registry'
import { classifyACPMessage } from './classification'
import { acpControlFeedbackRule, acpControlResponseBuilder, acpControlResponseSummary } from './controlResponse'
import { acpElicitation } from './elicitation'
import { acpExtractControl, acpPermissionSpanId } from './extractControl'
import { acpDividerReader } from './extractors/resultDivider'
import { createACPRowExtractor } from './extractors/row'
import { resolveACPMessage } from './extractors/toolCall'
import { acpSpanRole, createACPRelatedMessagesReader } from './spanRole'

/**
 * Per-provider settings configuration for an ACP provider. The discriminator
 * picks how the provider's plan-mode/writable axis is stored: providers that
 * use the agent's top-level `permissionMode` field (Cursor, Goose, Reasonix) use
 * `kind: 'permissionMode'`; providers that store it in `optionValues` under a
 * custom group key (OpenCode `primaryAgent`, Kilo) use `kind: 'optionGroup'`.
 *
 * The generic settings panel renders every reported option group on its own, so
 * this config only carries the data the registration logic needs: (per kind) the
 * default mode / writable group key + default.
 */
export type ACPSettingsPanelConfig
  = | { kind: 'permissionMode', defaultMode: PermissionMode }
    | { kind: 'optionGroup', optionGroupKey: string, defaultValue: string }

/**
 * Per-provider question-handling hooks. Providers that delegate `AskUserQuestion`
 * to the shared ACP path leave this unset; OpenCode/Kilo/Cursor each plug in
 * their own payload sniffer + extractor + responder.
 */
export type ACPQuestionHandling = ProviderAskUserQuestion

/**
 * Options accepted by {@link registerACPProvider}. Every value beyond
 * `provider`, the settings config, and the control components is derived from
 * the settings config — the `planMode` config (when `planValue` is supplied) falls
 * out of its `kind` plus its `defaultMode` / `defaultValue` / `optionGroupKey`.
 *
 * Supply EXACTLY ONE of `settingsConfig` or `defaultPermissionMode`: the latter is
 * sugar for the common `{ kind: 'permissionMode', defaultMode }` case (Cursor/
 * Goose/Reasonix), mirroring how {@link registerOpenCodeProtocolProvider} hides the
 * `optionGroup` kind behind `defaultPrimaryAgent`.
 */
export interface ACPProviderOptions {
  provider: AgentProvider
  /** The provider's own reading of one call, for the kind-discriminated pair. */
  toolCallAdapter?: ACPToolCallAdapter
  /** Explicit settings config (optionGroup or an explicit permissionMode). */
  settingsConfig?: ACPSettingsPanelConfig
  /** Sugar for `settingsConfig: { kind: 'permissionMode', defaultMode }`. */
  defaultPermissionMode?: PermissionMode
  /**
   * Control reader. Defaults to the shared {@link acpExtractControl}; a provider
   * whose payload is shaped differently (Cursor) passes its own.
   */
  extractControl?: ProviderControlCapability['extractControl']
  /**
   * The requests this provider answers ITSELF. Omit it for the whole family, which
   * answers every permission through the shared decision row.
   */
  controlActionsFor?: ProviderControlCapability['controlActionsFor']
  /**
   * How this provider sends one chosen option. Defaults to the selected-option
   * outcome, which is the Agent Client Protocol's own reply and which OpenCode and
   * Kilo answer with unchanged.
   */
  sendPermissionOption?: ProviderControlCapability['sendPermissionOption']
  /**
   * Mode value that represents "plan" for this provider's plan-mode toggle.
   * Omit to disable plan-mode wiring (e.g. Goose has no plan mode).
   */
  planValue?: string
  /** Provider-native presets for standard permission actions. */
  permissionPresets?: ProviderPermissionPresets
  /** Question-handling hooks for providers that override the default ACP path. */
  questionHandling?: ACPQuestionHandling
  /**
   * The provider's question reply carries the chosen options AND the reader's own
   * words for one question (Grok Build's `annotations[q].notes`), so the dialog keeps
   * both. Omit it for a provider whose reply takes one or the other.
   */
  preservesSelectionNotes?: boolean
  /**
   * Persisted control-response -> display derivation. Defaults to {@link acpControlResponseSummary}
   * (the permission-selection path); OpenCode/Kilo and Cursor pass their own, which dispatch on the
   * request shape and delegate back to the ACP default for the permission case.
   */
  controlResponseDisplay?: ProviderControlCapability['controlResponseDisplay']
  /**
   * Provider-specific classification of a `tool_call_update` session update.
   * Returns a `tool_use` category when the provider recognizes its own wire
   * shape in the update (e.g. Goose's subagent tool-request _meta), or
   * `undefined` to fall through to the shared status-based classifier.
   */
  classifyToolCallUpdate?: (parent: Record<string, unknown>) => MessageCategory | undefined
  /**
   * Attachment capabilities. Defaults to full support; pass a restricted set for
   * providers that can't take every attachment kind (e.g. Reasonix is text-only).
   */
  attachments?: AttachmentCapabilities
  /**
   * The MCP elicitation reader. Defaults to {@link acpElicitation}, the protocol's own
   * `elicitation/create`; a provider that raises an elicitation under a method of its
   * own (Grok Build) passes its reader.
   */
  elicitation?: ProviderControlCapability['elicitation']
  /**
   * The provider's own field for the reason of a rejected permission (Grok Build's
   * `_meta.followup_message`). Omit it, and the reason follows the reply as a message
   * of its own.
   */
  permissionRejectReason?: ACPPermissionRejectReason
  /**
   * The stop reason that the provider's own frame states for the end of a turn the
   * agent started by itself (Grok Build's `turn_completed`, Qwen Code's
   * `_qwencode/end_turn`), or undefined for any other frame. The worker stores that
   * frame as the turn-end row, and this reads it into the same divider as a prompt
   * response.
   */
  agentTurnEnd?: (parent: Record<string, unknown>) => string | undefined
  /**
   * The id of the agent's own reasoning-effort config option, when it is not the
   * well-known `effort`. See {@link ProviderConfigurationCapability.effortGroupKey}.
   */
  effortGroupKey?: string
}

/**
 * The option-group id the trigger renders as its mode segment, derived from the
 * settings config: the permission-mode field (Cursor/Goose/Reasonix), the custom
 * optionGroup key (OpenCode/Kilo primaryAgent). This matches planModeFromConfig's groupKey, but
 * it exists even when plan mode is not wired (Goose), so it is derived independently.
 */
function triggerModeGroupKeyForConfig(config: ACPSettingsPanelConfig): string {
  switch (config.kind) {
    case 'permissionMode':
      return OPTION_ID_PERMISSION_MODE
    case 'optionGroup':
      return config.optionGroupKey
  }
}

/** Synthesize the plan-mode config from a settingsConfig + planValue. */
function planModeFromConfig(
  config: ACPSettingsPanelConfig,
  planValue: string,
): NonNullable<ProviderConfigurationCapability['planMode']> {
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
  // Each hook below is composed from the options and imported readers only: the
  // registration rule refuses a hook that a local value supplies.
  const controls: ProviderControlCapability = {
    controlResponseDisplay: withElicitationResponse(opts.elicitation ?? acpElicitation, opts.controlResponseDisplay ?? acpControlResponseSummary),
    elicitation: opts.elicitation ?? acpElicitation,
    // The reply builder asks the provider's OWN control reader which requests are plan
    // approvals, so the composer answers each request the way the banner draws it.
    buildControlResponse: acpControlResponseBuilder(opts.extractControl ?? acpExtractControl, opts.permissionRejectReason),
    controlFeedbackAsFollowUpMessage: acpControlFeedbackRule(opts.extractControl ?? acpExtractControl, opts.permissionRejectReason),
    // The shared Agent Client Protocol reader; a provider whose payload is shaped
    // differently (Cursor) passes its own, which delegates back for the rest.
    extractControl: opts.extractControl ?? acpExtractControl,
    controlToolSpanId: acpPermissionSpanId,

    // Neither half of the banner needs a component from this family now. The reader
    // above fills the model, the shared row draws it, and the decision travels back as
    // the protocol's own selected-option outcome.
    sendPermissionOption: opts.sendPermissionOption ?? sendSelectedOptionResponse,

    ...(opts.controlActionsFor !== undefined ? { controlActionsFor: opts.controlActionsFor } : {}),
    ...(opts.permissionPresets !== undefined ? { permissionPresets: opts.permissionPresets } : {}),
    ...(opts.questionHandling ? { askUserQuestion: opts.questionHandling } : {}),
    ...(opts.preservesSelectionNotes ? { preservesSelectionNotes: true } : {}),
  }
  const configuration: ProviderConfigurationCapability = {
    attachments: opts.attachments ?? { text: true, image: true, pdf: true, binary: true },
    ...(opts.planValue !== undefined ? { planMode: planModeFromConfig(sc, opts.planValue) } : {}),
    triggerModeGroupKey: triggerModeGroupKeyForConfig(sc),
    ...(opts.effortGroupKey !== undefined ? { effortGroupKey: opts.effortGroupKey } : {}),
  }
  const plugin: ProviderPlugin = {
    transcript: {
      resolveMessage: resolveACPMessage,
      classify: classifyACPMessage({
        ...(opts.classifyToolCallUpdate ? { classifyToolCallUpdate: opts.classifyToolCallUpdate } : {}),
        ...(opts.agentTurnEnd ? { agentTurnEnd: opts.agentTurnEnd } : {}),
      }),
      spanRole: acpSpanRole,
      relatedMessages: createACPRelatedMessagesReader(opts.toolCallAdapter),
      extractRow: createACPRowExtractor(opts.toolCallAdapter),
      extractDivider: acpDividerReader(opts.agentTurnEnd),
    },
    controls,
    configuration,
  }

  registerProvider(opts.provider, plugin)
}
