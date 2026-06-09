// Provider registry. Each provider module (claude, codex, opencode, +stubs)
// calls registerProvider() at import time as a side effect; the side-effect imports
// live in providers/index.ts. providerFor() returns undefined if a provider
// was never imported, so callers that depend on a provider being registered must
// ensure providers/index.ts (or the specific provider module) is imported first.
//
// This mirrors the backend's `agent.Provider` interface and `agent.ProviderFor`
// lookup; each side carries the per-provider hooks its layer needs.

import type { Component, JSX } from 'solid-js'
import type { ActionsProps, AskQuestionState, ContentProps, Question } from '../controls/types'
import type { MessageCategory } from '../messageClassification'
import type { RenderContext } from '../messageRenderers'
import type { AgentInfo, AgentProvider, AvailableModel, AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { AgentSessionInfo } from '~/stores/agentSession.store'
import type { PermissionMode } from '~/utils/controlResponse'

/**
 * Discriminated update emitted by a provider settings panel. The host
 * (`AgentEditorPanel` → `useAgentOperations`) routes each kind to the
 * appropriate RPC; the panel itself doesn't know whether `permissionMode`
 * is a top-level wire field or stored in `extraSettings`.
 */
export type ProviderSettingChange
  = | { kind: 'model', value: string }
    | { kind: 'effort', value: string }
    | { kind: 'permissionMode', value: PermissionMode }
    | { kind: 'optionGroup', key: string, value: string }

/** Read-only settings state surfaced to provider panels. */
export interface ProviderSettingsState {
  disabled?: boolean
  settingsLoading?: boolean
  model?: string
  effort?: string
  permissionMode?: string
  extraSettings?: Record<string, string>
  availableModels?: AvailableModel[]
  availableOptionGroups?: AvailableOptionGroup[]
}

export interface ProviderSettingsPanelProps extends ProviderSettingsState {
  /** Single dispatcher for any settings panel change. */
  onChange?: (change: ProviderSettingChange) => void
}

export interface AttachmentCapabilities {
  text: boolean
  image: boolean
  pdf: boolean
  binary: boolean
}

export interface ClassificationInput extends ParsedMessageContent {
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
 * the copy button, not on every render. `hasCopyable` is a cheap presence
 * check used by the toolbar to decide whether to show the Copy button without
 * paying the formatting cost of the getter on every render.
 */
export interface ToolResultMeta {
  /** Result has more content than fits collapsed; toolbar shows expand button. */
  collapsible: boolean
  /** Result has a renderable diff; toolbar shows split/unified toggle. */
  hasDiff: boolean
  /** True iff `copyableContent()` would return a non-null string. */
  hasCopyable: boolean
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

/**
 * Provider-neutral data model for a `result_divider` (turn-end) message,
 * produced by a provider's {@link Provider.resultDivider} hook and drawn by the
 * single shared `ResultDivider` renderer. `isError` drives the inline danger
 * color; `detail` renders below the label as a `<pre>` block (Claude's error
 * detail). Providers that show detail inline (e.g. Codex's `message — details`)
 * bake it into `label` and leave `detail` unset.
 */
export interface ResultDividerModel {
  /** The divider label, e.g. "Turn ended", "Took 2.1s", "API Error: 529 …". */
  label: string
  /** Render in danger color (a failed/aborted turn). */
  isError?: boolean
  /** Optional multi-line detail block shown below the label. Omit (undefined), never empty. */
  detail?: string
}

export interface Provider {
  /** Default model identifier for this provider. */
  defaultModel?: string
  /** Default effort for this provider. */
  defaultEffort?: string
  /** Default permission mode identifier for this provider. */
  defaultPermissionMode?: PermissionMode
  /**
   * Extra per-provider settings to seed into a new agent's OpenAgent request.
   * Omit when the provider needs none. Codex seeds its collaboration mode.
   */
  defaultExtraSettings?: Record<string, string>
  /**
   * Fraction of the context window (as a percentage, e.g. 16.5) this provider
   * reserves as an autocompact buffer, subtracted from usable capacity when
   * computing the context-usage percentage. Omit (treated as 0) for providers
   * with no reserved headroom. Claude Code reserves a buffer.
   */
  contextBufferPct?: number
  /**
   * True when this provider's `agentSessionId` is a session FILE PATH rather
   * than an opaque id, so the UI shortens it to a basename for display and
   * labels the copy action "session file path". Pi uses session files.
   */
  sessionIdIsFilePath?: boolean
  /**
   * True when an AskUserQuestion option selection and the free-text note
   * coexist (the agent accepts both), so picking an option does NOT clear the
   * custom text and vice versa. Omit (mutually exclusive) for providers where
   * an answer and a note are alternatives. Codex preserves both.
   */
  preservesSelectionNotes?: boolean

  /** Classify a parsed message into a rendering category. */
  classify: (input: ClassificationInput, context?: ClassificationContext) => MessageCategory

  /**
   * Decide whether a persisted AGENT message should clear the live thinking-token
   * estimate (a per-phase reset). Called only for AGENT-source messages. Omit to
   * use the default "main-scope only" policy (clear when `parentSpanId === ''`),
   * which is correct for the streamed-text estimator that drives Codex/Pi/ACP: a
   * subagent's commit nests under a span and must not reset the primary counter,
   * and the backend applies the same gate. Claude overrides to always clear,
   * because its counter is real per-phase telemetry (not the estimator) and its
   * parentSpanId is not a clean main-vs-subagent signal (a system-injected
   * tool_use_id yields a non-empty parentSpanId on a main-agent message).
   */
  clearsThinkingTokensForMessage?: (msg: { parentSpanId: string }) => boolean

  /**
   * Render a message given its category and parsed content.
   * Return null to fall through to the default renderer chain.
   */
  renderMessage?: (
    category: MessageCategory,
    parsed: unknown,
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
   * Extract quotable text from a parsed message — used by MessageBubble to
   * decide whether to surface the Reply / Copy-as-markdown buttons and what
   * text to ship to the clipboard. Each provider knows its own wire format:
   * Codex reads `parent.item.text`, ACP-based providers read
   * `parent.content.text`, Claude walks `message.content[]`.
   *
   * Return null when the message has no quotable text (the toolbar then
   * hides Reply / Copy).
   */
  extractQuotableText?: (
    category: MessageCategory,
    parsed: ParsedMessageContent,
  ) => string | null

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
   * Convert a parsed `result_divider` message into the provider-neutral
   * {@link ResultDividerModel}. The shared `renderResultDivider` consults this
   * and draws the model with one `ResultDivider` component, so the divider
   * markup/styling lives in one place across providers. Returns null when the
   * message isn't a recognizable turn-end for this provider (the caller falls
   * back to the raw-JSON renderer).
   */
  resultDivider?: (parsed: unknown) => ResultDividerModel | null

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
   * `providers/acp/classification`.
   */
  buildControlResponse?: (
    payload: Record<string, unknown>,
    content: string,
    requestId: string,
  ) => unknown

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
    /** Apply a mode change via the unified change dispatcher. */
    setMode: (mode: string, onChange: (change: ProviderSettingChange) => void) => void
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

  /**
   * Inner-message `type` values that don't represent agent progress for
   * the chat-level working-state heuristic. The shared `isAgentWorking`
   * keeps scanning back when the most recent message has one of these
   * types instead of treating it as an activity signal — covers
   * provider-specific lifecycle / status / extension events.
   */
  nonProgressTypes?: ReadonlySet<string>

  /**
   * JSON-RPC method names that don't represent agent progress (transport
   * metadata or pure lifecycle signals). Counterpart to `nonProgressTypes`
   * for providers whose wire format dispatches by `method` rather than
   * `type` (Codex JSON-RPC).
   */
  nonProgressMethods?: ReadonlySet<string>

  /**
   * Provider-specific gate for the chat-level thinking indicator. Returns
   * true/false to take precedence over the message-history heuristic, or
   * null to fall through to the default. Codex uses this to gate on its
   * explicit `codexTurnId` so a freshly-created tab doesn't show as
   * thinking before any message arrives.
   */
  hasActiveTurn?: (
    agent: AgentInfo,
    sessionInfo: AgentSessionInfo | undefined,
  ) => boolean | null
}

const registry = new Map<number, Provider>()

export function registerProvider(provider: AgentProvider, plugin: Provider): void {
  registry.set(provider, plugin)
}

export function providerFor(provider: AgentProvider): Provider | undefined {
  return registry.get(provider)
}

/**
 * Resolve a message/agent's own provider plugin, with no Claude (or any other)
 * fallback. A nullish provider (an absent `agentProvider` field) and an
 * unregistered enum value both yield `undefined`: callers must treat a
 * missing plugin as a misconfiguration to surface (e.g. `unsupported_provider`)
 * rather than guessing another provider's renderers for this one's bytes. This
 * is the single chokepoint for that "dispatch strictly by provider" rule, so
 * the no-guessing contract lives in one place instead of a ternary at every
 * call site.
 */
export function pluginFor(provider: AgentProvider | undefined): Provider | undefined {
  return provider != null ? providerFor(provider) : undefined
}

/**
 * All registered providers, in insertion order. Used by shared heuristics
 * (e.g. `isAgentWorking`) that need to aggregate per-provider configuration
 * without hard-coding which providers exist. Callers must have already
 * triggered the side-effect imports in `providers/index.ts`.
 */
export function allRegisteredProviders(): Provider[] {
  return Array.from(registry.values())
}
