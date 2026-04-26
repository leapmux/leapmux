// Provider plugin registry. Each provider module (claude, codex, opencode, +stubs)
// calls registerProvider() at import time as a side effect; the side-effect imports
// live in providers/index.ts. getProviderPlugin() returns undefined if a provider
// was never imported, so callers that depend on a provider being registered must
// ensure providers/index.ts (or the specific provider module) is imported first.

import type { Component, JSX } from 'solid-js'
import type { ActionsProps, AskQuestionState, ContentProps, Question } from '../controls/types'
import type { MessageCategory } from '../messageClassification'
import type { RenderContext } from '../messageRenderers'
import type { AgentProvider, AvailableModel, AvailableOptionGroup, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { PermissionMode } from '~/utils/controlResponse'

export interface ProviderSettingsPanelProps {
  disabled?: boolean
  settingsLoading?: boolean
  model?: string
  effort?: string
  permissionMode?: string
  extraSettings?: Record<string, string>
  availableModels?: AvailableModel[]
  availableOptionGroups?: AvailableOptionGroup[]
  onModelChange?: (model: string) => void
  onEffortChange?: (effort: string) => void
  onPermissionModeChange?: (mode: PermissionMode) => void
  onOptionGroupChange?: (key: string, value: string) => void
}

export interface AttachmentCapabilities {
  text: boolean
  image: boolean
  pdf: boolean
  binary: boolean
}

export interface ClassificationInput extends ParsedMessageContent {
  messageRole: MessageRole
  agentProvider?: AgentProvider
  spanId?: string
  spanType?: string
  parentSpanId?: string
  seq?: bigint
  createdAt?: string
}

export interface ClassificationContext {
  hasCommandStream?: boolean
  commandStreamLength?: number
}

/**
 * Toolbar-relevant metadata for a tool_result-shaped message. Returned by the
 * provider plugin so MessageBubble doesn't need to know per-tool wire formats.
 *
 * `copyableContent` is a getter so the (potentially expensive) copyable text
 * — Edit's unified diff, for instance — is computed only when the user clicks
 * the copy button, not on every render.
 */
export interface ToolResultMeta {
  /** Result has more content than fits collapsed; toolbar shows expand button. */
  collapsible: boolean
  /** Result has a renderable diff; toolbar shows split/unified toggle. */
  hasDiff: boolean
  /** Lazily computed copyable text. Returns null when nothing is copyable. */
  copyableContent: () => string | null
}

/**
 * One entry inside a notification_thread wrapper, after the provider has
 * inspected a single message. The shared thread renderer concatenates entries
 * into the final markup; the `'group'` variant lets a provider opt into
 * collapse-by-`groupKey` (e.g. Codex MCP startup statuses grouped by state).
 */
export type NotificationThreadEntry
  = | { kind: 'text', text: string }
    | { kind: 'group', groupKey: string, prefix: string, entry: string }
    | { kind: 'divider', text: string, loading?: boolean }

export interface ProviderPlugin {
  /** Default model identifier for this provider. */
  defaultModel?: string
  /** Default effort for this provider. */
  defaultEffort?: string
  /** Default permission mode identifier for this provider. */
  defaultPermissionMode?: PermissionMode

  /** Classify a parsed message into a rendering category. */
  classify: (input: ClassificationInput, context?: ClassificationContext) => MessageCategory

  /**
   * Render a message given its category and parsed content.
   * Return null to fall through to the default renderer chain.
   */
  renderMessage?: (
    category: MessageCategory,
    parsed: unknown,
    role: MessageRole,
    context?: RenderContext,
  ) => JSX.Element | null

  /**
   * Compute toolbar metadata (collapsible, copyable content, diff presence)
   * for a tool_result-shaped message. Return null when this provider does not
   * produce metadata for the message — MessageBubble will then render its
   * toolbar with no per-tool affordances.
   *
   * Receives the parsed tool_use sibling so the provider can inspect both
   * halves (e.g. Claude pulls `file_path` from the input when the result
   * payload doesn't carry it).
   */
  toolResultMeta?: (
    category: MessageCategory,
    parsed: unknown,
    spanType: string | undefined,
    toolUseParsed: ParsedMessageContent | undefined,
  ) => ToolResultMeta | null

  /**
   * Build the wire-format content string to interrupt the agent.
   * Returns null if interrupt is not supported or not applicable.
   */
  buildInterruptContent?: (agentSessionId: string, codexTurnId?: string) => string | null

  /**
   * Returns true when the given control request payload represents an
   * "ask user question" interaction for this provider.
   */
  isAskUserQuestion?: (payload: Record<string, unknown>) => boolean

  /**
   * Convert one message inside a notification_thread wrapper into thread
   * entries. The shared `renderNotificationThread` consults each provider's
   * implementation before falling back to its own provider-neutral switch.
   *
   * Returns null when this provider doesn't recognize the message (the shared
   * switch tries next). Returns an empty array when the provider recognizes
   * the message but it produces no visible entries (e.g. all tiers below the
   * warning threshold).
   */
  notificationThreadEntry?: (msg: Record<string, unknown>) => NotificationThreadEntry[] | null

  /**
   * Extract `Question[]` from an `AskUserQuestion` control request payload.
   * Each provider's payload shape differs (Codex `params.questions`,
   * OpenCode `properties.questions`, Cursor's custom shape, Claude's
   * `getToolInput(payload).questions`).
   */
  extractAskUserQuestions?: (payload: Record<string, unknown>) => Question[]

  /**
   * Send the user's answers back as a control response. Receives the original
   * `payload` so providers that need echo fields off it (Claude reads them
   * via `getToolInput`) can access them without a separate API.
   */
  sendAskUserQuestionResponse?: (
    agentId: string,
    sendControlResponse: (agentId: string, bytes: Uint8Array) => Promise<void>,
    requestId: string,
    questions: Question[],
    askState: AskQuestionState,
    payload: Record<string, unknown>,
  ) => Promise<void>

  /**
   * Build the wire-format control-response object for a *non-AskUserQuestion*
   * control request. The shared layer serializes the result and ships it.
   *
   * Receives the editor `content` (empty when the user hit Send with no
   * input), the original `payload`, and the `requestId`. Each provider
   * decides whether the response is allow vs deny, what shape the response
   * takes, and whether to add provider-specific markers (e.g. Codex's
   * `codexPlanModePrompt` flag, or Claude's force-deny on `ExitPlanMode`).
   *
   * ACP-based providers can delegate to `acpBuildControlResponse` from
   * `providers/acpShared`.
   */
  buildControlResponse?: (
    payload: Record<string, unknown>,
    content: string,
    requestId: string,
  ) => Record<string, unknown>

  /**
   * The permission mode value that disables all approval prompts.
   * Used by the "& Bypass Permissions" button in approval controls.
   * E.g. "bypassPermissions" for Claude Code, "never" for Codex.
   */
  bypassPermissionMode?: PermissionMode

  /**
   * Change the agent's permission mode. Claude Code sends a lightweight
   * control_request (no restart), Codex restarts via UpdateAgentSettings.
   */
  changePermissionMode?: (
    workerId: string,
    agentId: string,
    mode: PermissionMode,
  ) => Promise<void>

  /**
   * Plan mode toggle configuration. Providers define how plan mode
   * maps to their native settings so the shared toggle logic stays
   * provider-agnostic.
   */
  planMode?: {
    /** Read the current plan-relevant mode from agent state. */
    currentMode: (agent: { permissionMode?: string, extraSettings?: Record<string, string> }) => string
    /** The value that represents "plan" mode. */
    planValue: string
    /** The default (non-plan) mode value. */
    defaultValue: string
    /** Apply a mode change via the appropriate callback. */
    setMode: (mode: string, callbacks: {
      onPermissionModeChange?: (mode: PermissionMode) => void
      onOptionGroupChange?: (key: string, value: string) => void
    }) => void
  }

  /** Optional control request content component for this provider. */
  ControlContent?: Component<ContentProps>

  /** Optional control request actions component for this provider. */
  ControlActions?: Component<ActionsProps>

  /** Optional settings panel component for this provider's agent settings dropdown. */
  SettingsPanel?: Component<ProviderSettingsPanelProps>

  /** Optional trigger label renderer for the settings dropdown button. */
  settingsTriggerLabel?: (props: ProviderSettingsPanelProps) => JSX.Element

  /** Optional extra class for the settings dropdown menu container. */
  settingsMenuClass?: string

  /** Attachment support for the provider. */
  attachments?: AttachmentCapabilities
}

const registry = new Map<number, ProviderPlugin>()

export function registerProvider(provider: AgentProvider, plugin: ProviderPlugin): void {
  registry.set(provider, plugin)
}

export function getProviderPlugin(provider: AgentProvider): ProviderPlugin | undefined {
  return registry.get(provider)
}
