// The provider plugin's capabilities, grouped by the reader that asks for them.
//
// A plugin groups its hooks into four capabilities.
// The transcript reader uses `transcript`. The permission presets use `controls`.
// The connection uses `session`. Agent settings use `configuration`.
// Each reader uses the capability that owns its work.
// A single interface of optional hooks let each reader access unrelated hooks.
// An unverified optional member could then remain undefined through four layers.
//
// Every registered provider requires `transcript` to read its wire format into rows.
// The other three capabilities remain optional where the provider supplies none.
// Their members remain optional only where the protocol supplies no value.
// A provider without a control channel omits `controls`.

import type { MessageCategory } from '../messageClassifier'
import type { AssistantTextSegment } from '../model/assistantText'
import type { TurnEnd } from '../model/divider'
import type { CompactionDetails, NotificationEntry } from '../model/notification'
import type { ChatRow } from '../model/row'
import type { ToolCall } from '../model/toolCall'
import type { ProviderPermissionPresets } from '../providerSettings'
import type { ResolvedMessageContent, RowExtractionInput } from '../rowExtractionTypes'
import type { AgentProvider, AssembledMessageKind, MessageCompletion, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolSpanRole } from '~/lib/messageSpan'
import type { ContextUsageInfo, RateLimitUpdate } from '~/models/agentSession'

export interface AttachmentCapabilities {
  text: boolean
  image: boolean
  pdf: boolean
  audio: boolean | readonly string[]
  binary: boolean | readonly string[]
}

export interface StartupOption {
  readonly value: string
  readonly label: string
  readonly description?: string
}

export interface StartupOptionGroup {
  readonly id: string
  readonly label: string
  readonly defaultValue: string
  readonly readOnlyReason: string
  readonly options: readonly StartupOption[]
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

  /** Identify a stored native compaction start for the history filter only. */
  compactionStart?: (message: Record<string, unknown>) => boolean

  /**
   * Split one assembled assistant text into segments this provider marks
   * itself. A provider whose model echoes an inline note about a steer the
   * reader sent supplies it; the note leaves the model's prose, and the
   * renderer draws it as a chip. Returns undefined for a text with no such
   * mark, and the row then renders its plain text.
   */
  assistantTextSegments?: (text: string) => AssistantTextSegment[] | undefined

  /** Classify a parsed message into a rendering category. */
  classify: (input: ClassificationInput, context?: ClassificationContext) => MessageCategory

  /**
   * Identify the message's span role from this provider's wire format.
   * `request` and `result` identify pair sides without depending on arrival order.
   * `none` explicitly supplies neither side. `other` leaves the role unknown for the index's fallback.
   */
  spanRole: (parsed: ResolvedMessageContent) => ToolSpanRole

  /**
   * Whether this row draws from its span's REQUEST, so the browser must hold the request
   * before the row shows and reports a height. A result row that states no tool name or
   * no arguments of its own answers true. Omit it for a provider whose rows are
   * self-contained.
   *
   * A row can need only the EARLIER side of its span. A request never needs its result:
   * the extraction input carries no result (see ToolSpanContext), so a row draws the
   * same content whether or not its result exists yet.
   */
  needsRequest?: (parsed: ResolvedMessageContent) => boolean

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

  /** Read reported file paths for the selected call without reading file contents. */
  outputFilePaths?: (input: RowExtractionInput, call: ToolCall) => readonly string[]

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
   * Every plugin that registers DIRECTLY supplies it, Amp excepted. The plugins of the
   * Agent Client Protocol family need none. Those daemons send JSON-RPC alone. A frame
   * that the worker persists verbatim is a session update or a request envelope, and
   * neither one is a notification. Every notification in those transcripts is LeapMux's
   * own envelope, which `leapmuxNotificationEntry` answers first, so this hook would
   * never run. `classifyACPMessage` accepts a `system` frame, and no daemon of this
   * family sends one. Amp needs none either: its stream carries no notification of its
   * own, so every notification in its transcripts is LeapMux's.
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
 * The control facts that a provider states in the browser: the presets of its
 * permission actions.
 *
 * Nothing here reads a control request, builds an answer, or words a saved answer.
 * The Worker reads each request and builds each answer (`Provider.ReadControlRequest`
 * and `Provider.AnswerControlRequest` in Go): the browser draws the neutral prompt
 * that the Worker sends, and sends back a neutral decision. A saved answer reads
 * from the prompt and the decision that the Worker records with it
 * (~/components/chat/controlDecisionSummary.ts).
 */
export interface ProviderControlCapability {
  /** Provider-native presets for standard permission actions. */
  permissionPresets?: ProviderPermissionPresets
}

/**
 * The session facts a provider states beside its transcript: what it consumed,
 * what it reserves, and how its sessions are named.
 */
export interface ProviderSessionCapability {
  /** The provider cannot confirm the current goal status. Show its objective without a status label. */
  goalStatusUnconfirmed?: boolean
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
   * Claude, Codex, Pi, Copilot, ZCode and Oh My Pi supply it. The other providers need
   * none, because the worker broadcasts the normalized `context_usage` for them, so the
   * wrapper answers before it reaches a plugin and this hook would never run:
   *
   *   - The Agent Client Protocol family: the worker reads the `usage_update` frame
   *     (`handleUsageUpdate` in `providers/acp/base.go`).
   *   - MiMo Code: the worker reads the token counts of each assistant message
   *     (`recordMessageUsage` in `providers/mimo/output.go`).
   *   - Kimi Code and Codewhale: the worker reads the context readout that the runtime
   *     reports, and states it on the turn.
   *   - Amp: the worker reads the usage of each assistant message
   *     (`recordUsage` in `providers/amp/output.go`).
   *   - Cline: the worker reads the usage that the hub reports for each model call
   *     (`handleUsageUpdated` in `providers/cline/output.go`).
   */
  contextUsageFromMessage?: (parsed: ParsedMessageContent) => ContextUsageInfo | null

  /**
   * Read a native compaction boundary, or null for another frame.
   * This runs with contextUsageFromMessage before row extraction and rendering.
   * The same provider parser supplies the notification and context meter.
   *
   * These providers supply the reader:
   * - Claude Code
   * - Codex
   * - Pi
   * - GitHub Copilot
   * - Kimi Code
   * - Oh My Pi
   * - Cline
   * - Qoder
   * - ZCode
   * - Muse
   * - Command Code
   * - Droid
   * - DeepSeek Harness
   * - Letta
   *
   * A native boundary can lack token counts. It still marks the end of compaction.
   * Without a valid post-compaction count, the context meter retains its last usage until a later usage report updates it.
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
  /** Choices that the user selects before a native host starts. */
  startupOptionGroups?: readonly StartupOptionGroup[]
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
   *   - `permissionMode` -- Claude, ZCode, MiMo Code, Kimi Code, Oh My Pi, Cline, and each
   *     plugin of the Agent Client Protocol family but OpenCode and Kilo.
   *   - `session_mode` -- Copilot.
   *   - `collaboration_mode` -- Codex.
   *   - `codewhale_mode` -- Codewhale.
   *   - `primaryAgent` -- OpenCode, Kilo.
   *   - `agent_mode` -- Amp, whose mode chooses the model and the effort.
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
   * The option-group id of the provider's reasoning-effort axis, which the status
   * bar draws as its effort chip. Omit it for the well-known `effort` id, which
   * every provider whose effort LeapMux manages uses, and which OpenCode and Kilo
   * report under that id.
   *
   * An Agent Client Protocol agent reports its effort axis under an id of its own,
   * and LeapMux keeps that id, because the agent takes a change under it:
   *
   *   - `thinking_effort` -- Goose.
   *   - `reasoning_effort` -- Grok Build, Qwen Code.
   *   - `effortLevel` -- Kiro.
   *
   * The plugin reads the id from its protocol contract, the same constant the
   * worker drives the axis through.
   */
  effortGroupKey?: string

  /**
   * True when a running agent of this provider permits direct child input.
   * Drives the composer gate for child tabs together with
   * `AgentInfo.accepts_messages`: the proto field WINS when present on the
   * tab; this is the fallback for optimistic state. Omit it for false.
   */
  supportsSubagentSend?: boolean
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
