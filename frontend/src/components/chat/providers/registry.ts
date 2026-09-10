// Provider registry. Each provider module (claude, codex, opencode, +stubs)
// calls registerProvider() at import time as a side effect; the side-effect imports
// live in providers/index.ts. providerFor() returns undefined if a provider
// was never imported, so callers that depend on a provider being registered must
// ensure providers/index.ts (or the specific provider module) is imported first.
//
// This mirrors the backend's `agent.Provider` interface and `agent.ProviderFor`
// lookup; each side carries the per-provider hooks its layer needs.

import type { LucideIcon } from 'lucide-solid'
import type { Component, JSX } from 'solid-js'
import type { ActionsProps, ContentProps, ControlAnswerState, Question } from '../controls/types'
import type { MessageCategory } from '../messageClassification'
import type { RenderContext } from '../messageRenderers'
import type { ControlResponseDeriver } from '../persistedControlResponse'
import type { ProviderPermissionPresets } from '../providerSettings'
import type { AgentProvider, AssembledMessageKind, MessageCompletion, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ContextUsageInfo, RateLimitInfo } from '~/stores/agentSession.store'
import type { ControlRequest } from '~/stores/control.store'

export interface AttachmentCapabilities {
  text: boolean
  image: boolean
  pdf: boolean
  binary: boolean
}

export interface ProviderAskUserQuestion {
  isRequest: (payload: Record<string, unknown>) => boolean
  extractQuestions: (payload: Record<string, unknown>) => Question[]
  /**
   * Answers ONE request instance.
   *
   * The request travels whole, rather than as an agent id, a request id and a
   * payload in three separate argument positions. Those three belong to one
   * instance, and a caller that sourced them separately could pair values from
   * two -- which is the exact pairing the worker's idempotency claim keys on.
   */
  sendAnswer: (
    request: ControlRequest,
    sendControlResponse: (bytes: Uint8Array) => Promise<void>,
    questions: Question[],
    answerState: ControlAnswerState,
  ) => Promise<void>
  sendReject: (
    request: ControlRequest,
    sendControlResponse: (bytes: Uint8Array) => Promise<void>,
    message: string,
  ) => Promise<void>
}

export interface ClassificationInput extends ParsedMessageContent {
  agentProvider?: AgentProvider
  /**
   * Who wrote the row. Read ONLY by the provider-neutral carve-outs in
   * `classifyMessage`, never by a plugin: a plugin recognizes its own wire
   * format by shape, and branching on the source instead would let it claim a
   * row LeapMux wrote.
   */
  source?: MessageSource
  assembledKind?: AssembledMessageKind
  completion?: MessageCompletion
  spanId?: string
  spanType?: string
  parentSpanId?: string
  seq?: bigint
  createdAt?: string
}

export interface ClassificationContext {
  /**
   * Whether these messages are a SUBAGENT's own transcript rather than the
   * transcript that spawned it.
   *
   * The same wire shape means different things on the two sides. A provider
   * that forwards a subagent's messages into the child transcript stamps every
   * one of them with the spawning tool_use id, exactly like the prompt the
   * PARENT sent to that subagent -- so the id alone cannot tell "the prompt to
   * a subagent" from "an ordinary message inside one", and only the transcript
   * can. A child transcript has no subagent of its own to prompt.
   */
  isChildTranscript?: boolean
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
    /**
     * A full-width labelled rule, drawn in the same style as a turn-end
     * divider. `loading` swaps the glyph for a spinner; `icon` overrides the
     * default compaction arrow (a subagent-end divider passes one glyph per
     * outcome). Omit `icon` and the shared renderer picks the compaction arrow.
     */
    | { kind: 'divider', text: string, loading?: boolean, icon?: LucideIcon }

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

/**
 * The role a message plays in a tool span: the tool_use `opener`, the `result`, or `other`
 * (the chatSpanIndex pairs a tool_use bubble with its result by this). Providers mark opener
 * vs result differently -- Claude by an Anthropic `tool_use`/`tool_result` content block, Pi by
 * a flat envelope `type` -- so the classifier is the per-provider {@link Provider.spanRole} hook.
 */
export type SpanRole = 'opener' | 'result' | 'other'

export interface Provider {
  /**
   * Extra per-provider settings to seed into a new agent's OpenAgent request.
   * Omit when the provider needs none. Codex seeds its collaboration mode.
   */
  defaultProviderOptions?: Record<string, string>
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
   *
   * This is a DISPLAY fact. What the resume field will ACCEPT is
   * {@link validateResumeHandle}, because a provider that reports a path may
   * still take an id as well.
   */
  sessionIdIsFilePath?: boolean
  /**
   * Validate a resume handle the user typed, returning an error message or
   * null. Omit to take the shared TOKEN rule (`validateSessionId`), which is
   * what Claude, Codex, ZCode and the ACP providers issue.
   *
   * A resume handle is NOT one shape across providers, and the SHAPE TEST that
   * picks a rule is a copy of that provider's own resolver — Pi reads a value
   * holding a separator, or ending in `.jsonl`, as a path, and anything else
   * as an id it matches inside the working directory. That is exactly the kind
   * of single-provider knowledge this interface exists to hold: the worker
   * already dispatches it per provider (`Provider.ResolveResumeHandle` in Go),
   * and while the browser hardcoded Pi's rule in `~/lib/validate` the two could
   * disagree in one place and not the other.
   *
   * An implementation composes the generic halves `validateSessionId` and
   * `validateSessionFilePath` rather than restating either.
   */
  validateResumeHandle?: (value: string) => string | null
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
   * Classify a message's role within a tool span (opener / result / other) from this provider's
   * wire shape, so chatSpanIndex can pair a tool_use with its result regardless of arrival order.
   * Claude reads Anthropic `tool_use`/`tool_result` content blocks; Pi routes by envelope `type`.
   * Omit for providers whose spans have no distinct opener/result marker (Codex / ACP emit only
   * tool_use openers) -- the caller defaults to `'other'` and files them first-seen-is-opener.
   */
  spanRole?: (parsed: ParsedMessageContent) => SpanRole

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
   * Every image a message carries, in a stable order.
   *
   * Two callers must agree on what "image N of this message" means: the row
   * that renders the images, and the image tab that resolves index N again
   * later against the same message re-fetched from the worker. One pure
   * provider-dispatched function is what stops them drifting -- an order
   * derived twice, from two walks of the same JSON, would agree until the day
   * one of them learned a new block kind.
   *
   * Runs outside the render tree, so it must not read Solid state. Return an
   * empty array when the message carries no image.
   */
  toolResultImages?: (
    parsed: unknown,
    spanType: string | undefined,
    toolUseParsed: ParsedMessageContent | undefined,
  ) => ImageResultSource[]

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
   * Short plaintext preview of a MARKED user-send row for the chat scroll rail's dot-hover
   * tooltip. Sibling of {@link extractQuotableText}: each provider knows its own wire shapes, so
   * preview extraction is a per-provider concern. Providers whose marked user sends are the
   * LeapMux-neutral `{content}` shape delegate to the shared `defaultMarkPreview`; Claude also
   * reads its Anthropic `message.content[]` tool_result blocks. Persisted control-response rows
   * are NOT previewed here -- they classify as `control_response` and the rail resolves their
   * preview through {@link controlResponseDisplay} (see chatMarkPreview.ts). Return null when
   * there is no previewable text (the rail then shows a mark-type label).
   */
  previewText?: (
    category: MessageCategory,
    parsed: ParsedMessageContent,
  ) => string | null

  /**
   * Derive the human-facing display for a persisted control-response row
   * (`{isSynthetic, controlResponse:{provider, requestId, request, response}}` -- see
   * persistedControlResponse.ts). Each provider knows its own native request/response wire shapes
   * (Codex JSON-RPC decisions, ACP permission options, OpenCode question answers, Pi extension-UI
   * responses, ...) and turns them into a label or a feedback block.
   *
   * The SINGLE label source for BOTH surfaces that show the row: the transcript renderer
   * (renderControlResponseRow, dispatched from renderMessageContent's shared `control_response`
   * branch) and the scroll rail's dot preview (messageMarkPreviewText), so the two cannot drift.
   * Return null when the payload isn't recognizable -- the caller then degrades via
   * {@link fallbackControlResponseDisplay} (coarse Approved/Rejected from the neutral behavior
   * envelope, else the generic "Responded" label).
   */
  controlResponseDisplay?: ControlResponseDeriver

  /** Complete support for the shared question UI. */
  askUserQuestion?: ProviderAskUserQuestion

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

  // --- Session-metadata extraction ------------------------------------------------------------
  // These hooks let the connection pipeline (useWorkspaceConnection) fold provider-native
  // notification / lifecycle / usage frames into the neutral AgentSessionInfo without parsing
  // any provider's wire shape in shared code. Each self-gates (returns null/false for a frame it
  // doesn't recognize) and returns neutral data; the shared caller owns the store writes.

  /**
   * Extract rate-limit tiers from a provider's rate-limit frame (Claude `rate_limit_event`,
   * Codex `account/rateLimits/updated`). Returns keyed entries the caller folds into
   * `AgentSessionInfo.rateLimits`, or null/[] when the frame carries none.
   */
  rateLimitsFromMessage?: (parsed: ParsedMessageContent) => { key: string, info: RateLimitInfo }[] | null

  /**
   * Extract this provider's context usage from a message, reading whatever shape carries it: a
   * Codex `thread/tokenUsage/updated` notification (`params.tokenUsage.last`), or a Claude/Pi
   * assistant message's raw `message.usage` (Claude `input_tokens`/`cache_*`, Pi
   * `input`/`cacheWrite`). Returns null for a message that carries no usage in this provider's
   * shape. The shared `extractContextUsage` wrapper owns the provider-neutral guards (subagent
   * skip, cost, backend-normalized `context_usage` preference) and only falls through to this hook
   * when no normalized usage is present, so the guards never live in a provider.
   */
  contextUsageFromMessage?: (parsed: ParsedMessageContent) => ContextUsageInfo | null

  /**
   * Derive a result-divider subtype the shared reader can't read off `inner.subtype` because it
   * lives in a provider-native shape (Codex's `turn.status` → `turn_completed`). Takes the whole
   * parsed message (like the sibling session-metadata hooks, unwrapping via getInnerMessage itself)
   * and returns undefined when the message carries no provider-specific subtype; the caller keeps the
   * neutral `inner.subtype`.
   */
  resultSubtype?: (parsed: ParsedMessageContent) => string | undefined

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

  /** Reports whether typed feedback needs a separate user message. */
  controlFeedbackAsFollowUpMessage?: (payload: Record<string, unknown>) => boolean

  /** Provider-native presets for standard permission actions. */
  permissionPresets?: ProviderPermissionPresets

  /**
   * Plan mode toggle configuration. Providers define which option group +
   * value represents "plan" mode so the shared toggle logic stays
   * provider-agnostic. Claude maps it to `permissionMode=plan`, Codex to
   * `collaboration_mode=plan`.
   */
  planMode?: {
    /** The option-group id whose value drives plan mode. */
    groupKey: string
    /** Read the current plan-relevant value from the agent's option values. */
    currentMode: (agent: { optionValues?: Record<string, string> }) => string
    /** The value that represents "plan" mode. */
    planValue: string
    /** The default (non-plan) value. */
    defaultValue: string
  }

  /**
   * The option-group id whose current value labels the settings-trigger's third
   * (mode) segment, after model and effort. Each provider identifies its single
   * mode-like axis -- permissionMode for Claude/Cursor/Copilot/Goose, the
   * collaboration_mode "Workflow" group for Codex, primaryAgent for OpenCode/Kilo
   * -- so the trigger renders ONE group's value rather than fusing several. Omit
   * for providers with no mode axis (Pi, Reasonix), which render no third segment.
   *
   * Distinct from `planMode` (the plan toggle): a provider can have a mode axis
   * without a plan toggle (Goose), so the trigger must not derive its segment from
   * planMode -- that coupling hid OpenCode's primary-agent segment whenever it
   * wasn't at the plan value.
   */
  triggerModeGroupKey?: string

  /** Optional control request content component for this provider. */
  ControlContent?: Component<ContentProps>

  /** Optional control request actions component for this provider. */
  ControlActions?: Component<ActionsProps>

  /** Attachment support for the provider. */
  attachments?: AttachmentCapabilities

  /**
   * True when a running agent of this provider permits direct child input.
   * Drives the composer gate for child tabs together with
   * `AgentInfo.accepts_messages`: the proto field WINS when present on the
   * tab; this is the fallback for optimistic state. Omit it for false.
   */
  supportsSubagentSend?: boolean

  /** True when the provider permits direct interruption of a child turn. */
  supportsSubagentInterrupt?: boolean
}

const registry = new Map<number, Provider>()

export function registerProvider(provider: AgentProvider, plugin: Provider): void {
  registry.set(provider, plugin)
}

export function providerFor(provider: AgentProvider): Provider | undefined {
  return registry.get(provider)
}

/**
 * Per-provider seed option selections for a fresh agent (e.g. Codex's collaboration
 * mode), shaped to spread directly into an OpenAgent request:
 * `...openAgentRequestOptions(provider)`. The plugin owns what (if anything) to seed via
 * its `defaultProviderOptions`; the worker fills every other axis with its provider
 * defaults. Returns `{}` when the provider seeds nothing, so the request omits `options`
 * rather than sending an empty map. Centralizing this keeps a new provider's seeding from
 * being wired into some agent-open paths but not others.
 */
export function openAgentRequestOptions(provider: AgentProvider): { options?: Record<string, string> } {
  const options = providerFor(provider)?.defaultProviderOptions
  return options ? { options } : {}
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
