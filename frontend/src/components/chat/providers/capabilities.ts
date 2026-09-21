// The provider plugin's capabilities, grouped by the reader that asks for them.
//
// A plugin is FOUR capabilities, not one wide interface of optional hooks. A reader
// that draws the transcript asks `transcript` and cannot reach the controls the
// banner owns; the connection pipeline asks `session` and holds nothing that reads
// a wire frame into a row. The wide interface let every reader see every hook, and
// an optional member nobody could prove present reached the screen as `undefined`
// paths through four layers.
//
// `transcript` is REQUIRED: every registered provider reads its own wire format into
// rows. The other three are optional capabilities, and the members inside each stay
// optional only where a protocol genuinely states none -- a provider with no
// control channel supplies no `controls` at all rather than an empty one.

import type { Component } from 'solid-js'
import type { SendPermissionOption } from '../controls/PermissionDecisionActions'
import type { ActionsProps, ControlAnswerState, ControlResponseSender } from '../controls/types'
import type { MessageCategory } from '../messageClassifier'
import type { ControlPrompt } from '../model/controlPrompt'
import type { TurnEnd } from '../model/divider'
import type { CompactionDetails, NotificationEntry } from '../model/notification'
import type { ControlQuestion } from '../model/question'
import type { ChatRow } from '../model/row'
import type { ControlResponseDeriver } from '../persistedControlResponse'
import type { ProviderPermissionPresets } from '../providerSettings'
import type { ResolvedMessageContent, RowExtractionInput } from '../rowExtractionTypes'
import type { ElicitationRequest } from '~/components/chat/model/controlPrompt'
import type { ToolSpanRole } from '~/components/chat/rowExtractionTypes'
import type { AgentProvider, AssembledMessageKind, MessageCompletion, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolSpanSide } from '~/lib/messageSpan'
import type { ContextUsageInfo, RateLimitUpdate } from '~/models/agentSession'
import type { ControlRequest } from '~/stores/control.store'

export interface AttachmentCapabilities {
  text: boolean
  image: boolean
  pdf: boolean
  binary: boolean
}

export interface ProviderAskUserQuestion {
  isRequest: (payload: Record<string, unknown>) => boolean
  extractQuestions: (payload: Record<string, unknown>, source?: ParsedMessageContent) => ControlQuestion[]
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
    sendControlResponse: ControlResponseSender,
    questions: ControlQuestion[],
    answerState: ControlAnswerState,
  ) => Promise<void>
  sendReject: (
    request: ControlRequest,
    sendControlResponse: ControlResponseSender,
    message: string,
  ) => Promise<void>
}

/**
 * What one provider's classifier reads: a RESOLVED parse plus the envelope
 * fields the shared carve-outs need. An intersection rather than an interface,
 * because an interface extending the branded parse drops the symbol member and
 * with it the compile-time boundary.
 */
export type ClassificationInput = ResolvedMessageContent & {
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
 * What {@link ProviderControlCapability.extractControl} answers: every control
 * shape but a question.
 *
 * A question takes its own path -- `askUserQuestion.isRequest` recognizes it and the
 * control surface asks that first -- so a reader that answered one here handed back a
 * value the caller dropped. Naming the type is what keeps that copy from returning.
 */
export type ExtractedControlRequest = Exclude<ControlPrompt, { kind: 'question' }>

export interface ControlExtractionInput {
  payload: Record<string, unknown>
  source?: ParsedMessageContent
  request?: ParsedMessageContent
}

/**
 * Reading a provider's own wire format into provider-NEUTRAL rows and frames.
 *
 * Every registered provider supplies this capability: reading the transcript is
 * what makes a provider a provider. The four REQUIRED members are the pipeline
 * (`classify`, `spanRole`, `extractRow`, `extractDivider`); the rest state the
 * linked messages and native notifications only some protocols carry.
 */
export interface ProviderTranscriptCapability {
  /**
   * Combine separate supplemental data with the provider payload for display only.
   *
   * It runs BEFORE classification, not inside {@link extractRow}: the classifier
   * reads the merged payload too -- a ZCode `result` frame whose recovered output
   * arrives in supplemental content classifies differently without it -- and
   * `messageContextResolver` performs the merge once for every reader of the row.
   */
  resolveMessage?: (parsed: ParsedMessageContent) => Record<string, unknown> | undefined

  /** Classify a parsed message into a rendering category. */
  classify: (input: ClassificationInput, context?: ClassificationContext) => MessageCategory

  /**
   * Classify a message's role within a tool span (request / result / other) from this provider's
   * wire shape, so chatSpanIndex can pair a tool_use with its result regardless of arrival order.
   * Claude reads Anthropic `tool_use`/`tool_result` content blocks; Pi routes by envelope `type`.
   */
  spanRole: (parsed: ResolvedMessageContent) => ToolSpanRole

  /** Linked messages that this row needs for rendering. Omit for self-contained rows. */
  relatedMessages?: (parsed: ResolvedMessageContent) => readonly ToolSpanSide[]

  /**
   * Read one message of this provider's wire format into the shared row model.
   *
   * Layer 1 of the render pipeline: the plugin is the only place that knows the
   * provider's format, and what it returns is provider-NEUTRAL, so the renderer below
   * it branches on the row kind alone. It produces no JSX and reads no Solid signal.
   *
   * Return `null` for a frame the provider cannot read at all; the caller then draws
   * the shared unrecognized card. A row the provider recognizes and deliberately
   * suppresses returns `{ kind: 'hidden' }`, which is a different statement.
   */
  extractRow: (input: RowExtractionInput) => ChatRow | null

  /**
   * Read one notification message of this provider's wire format into the shared
   * notification model.
   *
   * There is no shared switch below this. `notificationEntriesFor` asks ONE neutral
   * extractor first, so a plugin cannot claim a row LeapMux wrote.
   * `leapmuxNotificationEntry` answers every type LeapMux's own envelope carries, and
   * not the worker-written ones alone. Everything else is this provider's own frame.
   *
   * Return an empty array for a frame the provider recognizes and deliberately
   * suppresses. A frame it cannot read at all also yields nothing visible, which is
   * the safe answer for a notification: the row already reached the transcript.
   *
   * The five plugins that register DIRECTLY supply it -- Claude, Codex, Pi, Copilot and
   * ZCode. The five of the Agent Client Protocol family need none. Those daemons send
   * JSON-RPC alone. A frame the worker persists verbatim is a session update or a
   * request envelope, and neither one is a notification. Every notification in those
   * transcripts is LeapMux's own envelope, which `leapmuxNotificationEntry` answers
   * first, so this hook would never run. `classifyACPMessage` accepts a `system` frame,
   * and no daemon of this family sends one.
   */
  notificationEntry?: (msg: Record<string, unknown>) => NotificationEntry[]

  /**
   * Read a turn-end frame into the shared {@link TurnEnd}.
   *
   * A turn end is a cross-provider surface: every provider ends a turn and each states
   * it in its own frame -- a Claude `result` subtype, a Codex `turn.status`, a ZCode
   * `resultType`, an Agent Client Protocol `stopReason`. The plugin reads ITS frame and
   * states the label; `extractChatRow` adds the totals the worker measured, which are
   * the same fields for every provider. Returns null for a frame this provider does not
   * recognize as a turn end, and the row then draws the shared unrecognized card.
   *
   * `completion` is LeapMux's OWN reading of how the turn ended, which a provider
   * frame can contradict: Claude reports an interrupted turn with the same error
   * subtype it uses for a genuine failure, and only LeapMux knows it asked for the
   * stop. A plugin that needs no such correction ignores the parameter.
   */
  extractDivider: (parsed: unknown, completion?: MessageCompletion) => TurnEnd | null
}

/**
 * Answering the requests a provider's agent makes of the reader: permissions,
 * plans, questions, elicitation, and the responses those leave behind.
 */
export interface ProviderControlCapability {
  /**
   * Read one control request into the shared control model.
   *
   * The ONE reader of a provider's control payload. The banner switches over the model
   * and draws it; `buildControlResponse` and `askUserQuestion` answer it. Before this
   * hook every provider shipped its own `ControlContent` and `ControlActions`
   * components, and the five shared bodies they all dispatched to drifted apart in
   * which fields each provider bothered to pass.
   *
   * Returns null for a payload the provider does not recognize, which the caller
   * draws as the generic permission row -- the same answer an unnamed tool takes in
   * the transcript.
   *
   * A QUESTION is not one of the shapes this answers. `askUserQuestion.isRequest` is
   * the one recognizer, and the control surface asks it first -- so every provider
   * that also recognized one here was a second, weaker copy of that test, and the
   * caller threw the answer away. The return type states it, so the copy cannot
   * come back.
   */
  extractControl?: (input: ControlExtractionInput) => ExtractedControlRequest | null

  /**
   * The tool call one control request is about, as a span id, or "" for a request
   * that identifies none.
   *
   * The Agent Client Protocol family states its permission's tool call in a compact
   * form and keeps the ARGUMENTS on that call's own row, so the banner has to load
   * that row. The path to the id is the provider's own wire shape, which is why the
   * plugin answers it rather than the loader.
   */
  controlToolSpanId?: (payload: Record<string, unknown>) => string

  /** Complete support for the shared question UI. */
  askUserQuestion?: ProviderAskUserQuestion

  /**
   * True when an AskUserQuestion option selection and the free-text note
   * coexist (the agent accepts both), so picking an option does NOT clear the
   * custom text and vice versa. Omit (mutually exclusive) for providers where
   * an answer and a note are alternatives. Codex preserves both.
   */
  preservesSelectionNotes?: boolean

  /** Extract a native MCP input request for the shared form. */
  elicitation?: (payload: Record<string, unknown>, source?: ParsedMessageContent) => ElicitationRequest | undefined

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
   * `fallbackControlResponseSummary` (coarse Approved/Rejected from the neutral behavior
   * envelope, else the generic "Responded" label).
   */
  controlResponseDisplay?: ControlResponseDeriver

  /**
   * Build the wire-format control-response object for a *non-AskUserQuestion*
   * control request. The shared layer serializes the result and ships it.
   *
   * Receives the editor `content` (empty when the user hit Send with no
   * input), the original `payload`, and the `requestId`. Each provider
   * selects the native response shape and its decision or feedback fields.
   * LeapMux settings travel separately through ControlResponseOptions.
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

  /** Select the shared editor's purpose when the request uses native controls. */
  controlEditorPurpose?: (payload: Record<string, unknown>) => 'answer' | 'feedback' | 'none'

  /**
   * The actions for a request this provider answers ITSELF, or undefined to let
   * the shared switch answer it from {@link extractControl}'s model.
   *
   * Three providers answer some request of their own. Codex states its decisions
   * as WORDS that vary per request, and one of them carries a policy amendment as
   * an object rather than an id. Pi answers a dialog with three different
   * envelopes, none of them a permission. Cursor's create-plan sends a plan
   * verdict that its own worker transforms. Everything else answers through the
   * shared plan and permission rows, so it states nothing here.
   *
   * It takes the PAYLOAD because the answer is per-request, not per-provider:
   * Cursor answers its create-plan itself and lets the shared row answer every
   * Agent Client Protocol permission beside it.
   */
  controlActionsFor?: (payload: Record<string, unknown>) => Component<ActionsProps> | undefined

  /**
   * How this provider sends ONE chosen permission option.
   *
   * A provider whose `extractControl` states a non-empty option list must state
   * this beside it: the reader picks one of the runtime's own answers, and the id
   * travels back in that runtime's own envelope.
   */
  sendPermissionOption?: SendPermissionOption

  /** Provider-native presets for standard permission actions. */
  permissionPresets?: ProviderPermissionPresets
}

/**
 * The session facts a provider states beside its transcript: what it consumed,
 * what it reserves, and how its sessions are named.
 */
export interface ProviderSessionCapability {
  /**
   * Extract an explicit rate-limit merge or replacement from a provider frame.
   * Returns null when the frame carries no rate-limit update.
   */
  rateLimitsFromMessage?: (parsed: ParsedMessageContent) => RateLimitUpdate | null

  /**
   * Extract this provider's context usage from a message, reading whatever shape carries it: a
   * Codex `thread/tokenUsage/updated` notification (`params.tokenUsage.last`), or a Claude/Pi
   * assistant message's raw `message.usage` (Claude `input_tokens`/`cache_*`, Pi
   * `input`/`cacheWrite`). Returns null for a message that carries no usage in this provider's
   * shape. The shared `extractContextUsage` wrapper owns the provider-neutral guards (subagent
   * skip, cost, backend-normalized `context_usage` preference) and only falls through to this hook
   * when no normalized usage is present, so the guards never live in a provider.
   *
   * The five plugins that register DIRECTLY supply it -- Claude, Codex, Pi, Copilot and
   * ZCode. The five of the Agent Client Protocol family need none: the worker reads
   * their `usage_update` frame and broadcasts the normalized `context_usage` for it
   * (`handleUsageUpdate` in `acp_common.go`), so the wrapper answers before it reaches
   * a plugin and this hook would never run.
   */
  contextUsageFromMessage?: (parsed: ParsedMessageContent) => ContextUsageInfo | null

  /**
   * Read a CONTEXT-COMPACTION boundary out of this provider's frame, or null when the
   * frame is not one.
   *
   * A sibling of {@link contextUsageFromMessage}, and it runs on the same path: the
   * context-usage grid refreshes the instant a boundary lands, OUTSIDE the render tree
   * and before any row model exists. One provider-owned parse serves that reader and the
   * notification extractor, so `messageParser` holds no provider shape and the two
   * readers cannot disagree about what a boundary is.
   *
   * Four plugins supply it -- Claude, Codex, Pi and Copilot -- and the six that omit it
   * have no boundary to read. The five of the Agent Client Protocol family carry none
   * on that stream: OpenCode compacts behind an internal agent and reports a
   * `compacting` status alone, which states no size. ZCode emits no boundary either.
   * The grid therefore holds its pre-compaction reading for those six until the next
   * message that carries usage replaces it, which is a limit of the protocol rather
   * than a hook anybody forgot.
   */
  compactionBoundaryFromMessage?: (parsed: ParsedMessageContent) => CompactionDetails | null

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
}

/**
 * How a provider is configured and what its agents accept: attachments,
 * default options, the plan toggle, and child-agent capabilities.
 */
export interface ProviderConfigurationCapability {
  /** Attachment support for the provider. */
  attachments?: AttachmentCapabilities

  /**
   * Extra per-provider settings to seed into a new agent's OpenAgent request.
   * Omit when the provider needs none. Codex seeds its collaboration mode.
   */
  defaultProviderOptions?: Record<string, string>

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
   * mode-like axis, so the trigger renders ONE group's value rather than fusing
   * several:
   *
   *   - `permissionMode` -- Claude, ZCode, Cursor, Goose, Reasonix.
   *   - `session_mode` -- Copilot.
   *   - `collaboration_mode` -- Codex.
   *   - `primaryAgent` -- OpenCode, Kilo.
   *
   * Pi alone omits it, and renders no third segment.
   *
   * Distinct from `planMode` (the plan toggle): a provider can have a mode axis
   * without a plan toggle (Goose), so the trigger must not derive its segment from
   * planMode -- that coupling hid OpenCode's primary-agent segment whenever it
   * wasn't at the plan value.
   */
  triggerModeGroupKey?: string

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

/**
 * One provider's plugin: its transcript reading, and the capabilities only some
 * protocols carry.
 */
export interface ProviderPlugin {
  transcript: ProviderTranscriptCapability
  controls?: ProviderControlCapability
  session?: ProviderSessionCapability
  configuration?: ProviderConfigurationCapability
}
