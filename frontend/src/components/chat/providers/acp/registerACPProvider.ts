import type { Component } from 'solid-js'
import type { ActionsProps, ContentProps } from '../../controls/types'
import type { MessageCategory } from '../../messageClassification'
import type { ProviderPermissionPresets } from '../../providerSettings'
import type { AttachmentCapabilities, Provider, ProviderAskUserQuestion } from '../registry'
import type { ACPToolAdapter } from './toolPresentation'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { PermissionMode } from '~/utils/controlResponse'
import { withElicitationResponse } from '../../controls/elicitationResponse'
import { defaultMarkPreview } from '../../markPreviewShared'
import { buildPlanMode, OPTION_ID_PERMISSION_MODE } from '../../settingsGroups'
import { registerProvider } from '../registry'
import { ACPControlActions, ACPControlContent } from './ACPControlRequest'
import { acpBuildControlResponse, acpExtractQuotableText, classifyACPMessage } from './classification'
import { acpControlResponseDisplay } from './controlResponse'
import { acpElicitation } from './elicitation'
import { acpToolResultImages } from './extractors/image'
import { acpResultDivider } from './renderers'
import { renderACPMessage } from './rendering'
import { acpToolFinished, acpToolNeedsResult, resolveACPMessage } from './toolPresentation'
import { acpToolResultMeta } from './toolResult'

/**
 * Per-provider settings configuration for an ACP provider. The discriminator
 * picks how the provider's plan-mode/writable axis is stored: providers that
 * use the agent's top-level `permissionMode` field (Copilot, Cursor, Goose) use
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
 * sugar for the common `{ kind: 'permissionMode', defaultMode }` case (Copilot/
 * Cursor/Goose), mirroring how {@link registerOpenCodeProtocolProvider} hides the
 * `optionGroup` kind behind `defaultPrimaryAgent`.
 */
export interface ACPProviderOptions {
  provider: AgentProvider
  /** Extract native tool fields for the shared renderer. */
  toolAdapter?: ACPToolAdapter
  /** Explicit settings config (optionGroup or an explicit permissionMode). */
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
  /** Provider-native presets for standard permission actions. */
  permissionPresets?: ProviderPermissionPresets
  /** Question-handling hooks for providers that override the default ACP path. */
  questionHandling?: ACPQuestionHandling
  /**
   * Persisted control-response -> display derivation. Defaults to {@link acpControlResponseDisplay}
   * (the permission-selection path); OpenCode/Kilo and Cursor pass their own, which dispatch on the
   * request shape and delegate back to the ACP default for the permission case.
   */
  controlResponseDisplay?: Provider['controlResponseDisplay']
  /** Extra `session/update` types that should be hidden from the chat. */
  extraHiddenSessionUpdates?: Set<string>
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
 * settings config: the permission-mode field (Copilot/Cursor/Goose), the custom
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
): NonNullable<Provider['planMode']> {
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
    resolveMessage: resolveACPMessage,
    attachments: opts.attachments ?? { text: true, image: true, pdf: true, binary: true },

    classify: classifyACPMessage({
      ...(opts.extraHiddenSessionUpdates ? { extraHiddenSessionUpdates: opts.extraHiddenSessionUpdates } : {}),
      ...(opts.classifyToolCallUpdate ? { classifyToolCallUpdate: opts.classifyToolCallUpdate } : {}),
    }),
    spanRole: (parsed) => {
      const tool = parsed.parentObject
      if (tool?.sessionUpdate === 'tool_call')
        return acpToolFinished(tool, parsed.completion) ? 'result' : 'opener'
      if (tool?.sessionUpdate === 'tool_call_update' && acpToolFinished(tool, parsed.completion))
        return 'result'
      return 'other'
    },
    relatedMessages: (parsed) => {
      const tool = parsed.parentObject
      if (!tool)
        return []
      if (acpToolFinished(tool, parsed.completion))
        return ['request']
      return tool.sessionUpdate === 'tool_call' && acpToolNeedsResult(tool, opts.toolAdapter, parsed.supplementalContent) ? ['result'] : []
    },
    renderMessage: (category, parsed, context) => renderACPMessage(category, parsed, context, opts.toolAdapter),
    toolResultMeta: (category, input) => acpToolResultMeta(category, input, opts.toolAdapter),
    toolResultImages: input => acpToolResultImages(input, opts.toolAdapter),
    resultDivider: acpResultDivider,
    extractQuotableText: acpExtractQuotableText,
    // ACP-based providers (OpenCode, Cursor, Copilot, ...) mark only user sends and control-response
    // answers. A user send is the LeapMux-neutral `{content}` shape the shared extractor handles; a
    // control answer is the structured `{controlResponse}` row, which classifies as
    // `control_response` and resolves through controlResponseDisplay (below), not here.
    previewText: defaultMarkPreview,
    controlResponseDisplay: withElicitationResponse(acpElicitation, opts.controlResponseDisplay ?? acpControlResponseDisplay),
    elicitation: acpElicitation,
    buildControlResponse: acpBuildControlResponse,

    // Default to the shared ACP control UI; a provider whose payload is shaped
    // differently (Cursor) passes its own dispatching components.
    ControlContent: opts.ControlContent ?? ACPControlContent,
    ControlActions: opts.ControlActions ?? ACPControlActions,
  }

  if (opts.permissionPresets !== undefined)
    plugin.permissionPresets = opts.permissionPresets
  if (opts.planValue !== undefined)
    plugin.planMode = planModeFromConfig(sc, opts.planValue)
  plugin.triggerModeGroupKey = triggerModeGroupKeyForConfig(sc)

  if (opts.questionHandling) {
    plugin.askUserQuestion = opts.questionHandling
  }

  registerProvider(opts.provider, plugin)
}
