import type { MessageCategory } from '../../messageClassification'
import type { ProviderPermissionPresets } from '../../providerSettings'
import type { AttachmentCapabilities, ProviderAskUserQuestion, ProviderConfigurationCapability, ProviderControlCapability, ProviderPlugin } from '../capabilities'
import type { ACPToolCallAdapter } from './extractors/toolCall'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { PermissionMode } from '~/utils/controlResponse'
import { withElicitationResponse } from '../../controls/elicitationResponse'
import { sendSelectedOptionResponse } from '../../controls/types'
import { buildPlanMode, OPTION_ID_PERMISSION_MODE } from '../../settingsGroups'
import { registerProvider } from '../registry'
import { acpBuildControlResponse, classifyACPMessage } from './classification'
import { acpControlResponseDisplay } from './controlResponse'
import { acpElicitation } from './elicitation'
import { acpExtractControl, acpPermissionSpanId } from './extractControl'
import { acpResultDivider } from './extractors/resultDivider'
import { acpExtractRow } from './extractors/row'
import { acpToolCallNeedsResult, acpToolFinished, resolveACPMessage } from './extractors/toolCall'
import { ACP_SESSION_UPDATE } from './updateVocabulary'

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
   * Persisted control-response -> display derivation. Defaults to {@link acpControlResponseDisplay}
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
  const controls: ProviderControlCapability = {
    controlResponseDisplay: withElicitationResponse(acpElicitation, opts.controlResponseDisplay ?? acpControlResponseDisplay),
    elicitation: acpElicitation,
    buildControlResponse: acpBuildControlResponse,
    // The shared Agent Client Protocol reader; a provider whose payload is shaped
    // differently (Cursor) passes its own, which delegates back for the rest.
    extractControl: opts.extractControl ?? acpExtractControl,
    controlToolSpanId: acpPermissionSpanId,

    // Neither half of the banner needs a component from this family now. The reader
    // above fills the IR, the shared row draws it, and the decision travels back as
    // the protocol's own selected-option outcome.
    sendPermissionOption: opts.sendPermissionOption ?? sendSelectedOptionResponse,

    ...(opts.controlActionsFor !== undefined ? { controlActionsFor: opts.controlActionsFor } : {}),
    ...(opts.permissionPresets !== undefined ? { permissionPresets: opts.permissionPresets } : {}),
    ...(opts.questionHandling ? { askUserQuestion: opts.questionHandling } : {}),
  }
  const configuration: ProviderConfigurationCapability = {
    attachments: opts.attachments ?? { text: true, image: true, pdf: true, binary: true },
    ...(opts.planValue !== undefined ? { planMode: planModeFromConfig(sc, opts.planValue) } : {}),
    triggerModeGroupKey: triggerModeGroupKeyForConfig(sc),
  }
  const plugin: ProviderPlugin = {
    transcript: {
      resolveMessage: resolveACPMessage,
      classify: classifyACPMessage(
        opts.classifyToolCallUpdate ? { classifyToolCallUpdate: opts.classifyToolCallUpdate } : {},
      ),
      spanRole: (parsed) => {
        const tool = parsed.parentObject
        if (tool?.sessionUpdate === ACP_SESSION_UPDATE.TOOL_CALL)
          return acpToolFinished(tool, parsed.completion) ? 'result' : 'request'
        if (tool?.sessionUpdate === ACP_SESSION_UPDATE.TOOL_CALL_UPDATE && acpToolFinished(tool, parsed.completion))
          return 'result'
        return 'other'
      },
      relatedMessages: (parsed) => {
        const tool = parsed.parentObject
        if (!tool)
          return []
        if (acpToolFinished(tool, parsed.completion))
          return ['request']
        return (tool.sessionUpdate === ACP_SESSION_UPDATE.TOOL_CALL && acpToolCallNeedsResult(tool, opts.toolCallAdapter, parsed.supplementalContent))
          ? ['result']
          : []
      },
      extractRow: input => acpExtractRow(input, opts.toolCallAdapter),
      extractDivider: acpResultDivider,
    },
    controls,
    configuration,
  }

  registerProvider(opts.provider, plugin)
}
