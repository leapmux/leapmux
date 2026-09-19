import type {} from '../../registry'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolSpanRole } from '~/lib/messageSpan'
import type { TodoItem } from '~/models/todo'
import { ZCODE_EVENT, ZCODE_STORED_PART, ZCODE_STORED_PART_STATUS, ZCODE_STORED_PART_TYPE, ZCODE_STORED_TOOL, ZCODE_SUPPLEMENT, ZCODE_SUPPLEMENT_PAYLOAD, ZCODE_TOOL_KIND, ZCODE_TOOL_PREFIX } from '~/generated/contracts/zcode-protocol'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { rawTodosToItems } from '~/models/todo'
import { parseToolOutcome } from '../../../toolOutcome'
import { retainedRowIsFinal } from '../../registry'
import { zcodeToolSupplement } from '../toolSupplement'

/**
 * The `payload` of a `tool.updated` event, normalized across its six kinds.
 *
 * The kinds carry different fields, and the split matters most on the result side:
 *
 *   scheduled → {toolCallId, toolName, input, inputOmitted, inputRef}
 *   started   → {toolCallId, toolName, startedAt}
 *   progress  → {toolCallId, outputBytes, stdoutTail, stderrTail}
 *   result    → {toolCallId, result, duration}          // NO toolName
 *   error     → {toolCallId, error, duration}           // NO toolName
 *   batch     → {toolCallIds, successCount, errorCount}
 *
 * A `result` carries no tool name, so the result renderers resolve the name from the span
 * (`RenderContext.spanType`, which the worker sets from the tool name) or from the
 * paired `scheduled` row -- never from the result payload itself.
 */
export interface ZCodeToolUpdate {
  kind: string
  toolCallId: string
  /** Empty on a result/error/progress payload. Use `zcodeToolName` instead. */
  toolName: string
  input: Record<string, unknown>
  result: ZCodeToolResult | null
  /** The `error` object of an `error` kind, else null. */
  error: Record<string, unknown> | null
  isError: boolean
  durationMs: number | null
}

/**
 * The `result` object of a finished tool call.
 *
 * `content` is the text the model received. `display` is the app-server's own
 * rendering hint, and it is the ONLY place a structured diff arrives. `perf.detail`
 * carries the per-kind telemetry a command's exit code lives in.
 *
 * `content` is a string. The app-server converts inline media parts to text placeholders.
 * Computer-use and Node results can also supply images through their display hints.
 */
export interface ZCodeToolResult {
  success: boolean
  content: string
  display: Record<string, unknown> | null
  perfDetail: Record<string, unknown> | null
  truncated: boolean
  originalBytes: number | null
  returnedBytes: number | null
}

/**
 * The native session-event envelope. Extractors use one reader for its payload.
 */
export interface ZCodeEnvelope {
  type: string
  payload: Record<string, unknown>
}

/**
 * Unwrap a persisted ZCode row into its event type and payload.
 *
 * The two key names are contract constants, because the WORKER writes this same
 * envelope for a retained tool row (contracts/zcode-protocol.json `supplement`) and
 * the Go tags are pinned to the same table. A rename on one side alone left every
 * retained row drawing with no native record and nothing to say why.
 */
export function zcodeEnvelope(parsed: unknown): ZCodeEnvelope | null {
  if (!isObject(parsed))
    return null
  const type = pickString(parsed, ZCODE_SUPPLEMENT.Type)
  if (!type)
    return null
  return { type, payload: pickObject(parsed, ZCODE_SUPPLEMENT.Payload) ?? {} }
}

function zcodeToolResult(result: Record<string, unknown>): ZCodeToolResult {
  const perf = pickObject(result, 'perf')
  return {
    // The app-server omits `success` on some result shapes; an absent flag with a
    // present result means it succeeded, so only an explicit `false` is a failure.
    success: result.success !== false,
    content: pickString(result, 'content'),
    display: pickObject(result, 'display'),
    perfDetail: pickObject(perf, 'detail'),
    truncated: result.truncated === true,
    originalBytes: pickNumber(result, 'originalBytes'),
    returnedBytes: pickNumber(result, 'returnedBytes'),
  }
}

/**
 * Join the two output tails of a `progress` payload into one block of text.
 *
 * The app-server cuts each tail at a byte count, not at a line end, so a stdout tail
 * often stops in the middle of a line. A plain concatenation then glues the first
 * stderr line onto that partial line and shows one line that neither stream wrote.
 * A line break separates them, and only when the stdout tail does not already end in
 * one -- a second break would print a blank line the command never wrote.
 *
 * An absent or empty tail contributes nothing, so a call with output on one stream
 * alone keeps exactly the text of that stream.
 */
function joinOutputTails(stdout: string, stderr: string): string {
  if (!stdout || !stderr)
    return stdout || stderr
  return stdout.endsWith('\n') ? stdout + stderr : `${stdout}\n${stderr}`
}

/**
 * The partial output of a call that reported no result of its own.
 *
 * A `progress` payload carries the output tails, and the worker stores that frame as
 * the transcript row for a call a turn end cut short. The outcome is unknown, so this
 * claims no failure: the row's LeapMux completion states that the call did not
 * finish, and the shared renderer draws the Interrupted or Failed header from it.
 */
function zcodeProgressResult(payload: Record<string, unknown>): ZCodeToolResult | null {
  const content = joinOutputTails(pickString(payload, 'stdoutTail'), pickString(payload, 'stderrTail'))
  if (!content)
    return null
  return {
    success: true,
    content,
    display: null,
    perfDetail: null,
    truncated: false,
    originalBytes: null,
    returnedBytes: null,
  }
}

// Cache by envelope identity because the body and toolbar read the same event.
// The WeakMap releases an entry with its envelope.
const updateCache = new WeakMap<Record<string, unknown>, ZCodeToolUpdate | null>()

/**
 * Unwrap a persisted `tool.updated` row. Returns null for any other row, so a
 * caller can use it as its own type guard.
 */
export function zcodeExtractTool(parsed: unknown): ZCodeToolUpdate | null {
  if (!isObject(parsed))
    return null
  const cached = updateCache.get(parsed)
  if (cached !== undefined)
    return cached
  const update = buildZCodeToolUpdate(parsed)
  updateCache.set(parsed, update)
  return update
}

function buildZCodeToolUpdate(parsed: Record<string, unknown>): ZCodeToolUpdate | null {
  const envelope = zcodeEnvelope(parsed)
  if (!envelope || envelope.type !== ZCODE_EVENT.ToolUpdated)
    return null
  const payload = envelope.payload
  const toolCallId = pickString(payload, 'toolCallId')
  if (!toolCallId)
    return null
  const rawResult = pickObject(payload, 'result')
  const rawError = pickObject(payload, 'error')
  const result = rawResult ? zcodeToolResult(rawResult) : zcodeProgressResult(payload)
  return {
    kind: pickString(payload, 'kind'),
    toolCallId,
    toolName: pickString(payload, 'toolName'),
    input: pickObject(payload, 'input') ?? {},
    result,
    error: rawError,
    // Three independent signals, and any one of them is a failure: the `error` kind,
    // an `error` object, and a result that declares `success: false`.
    isError: pickString(payload, 'kind') === ZCODE_TOOL_KIND.Error
      || rawError !== null
      || result?.success === false,
    durationMs: pickNumber(payload, 'duration'),
  }
}

/**
 * One transcript row, as every ZCode extractor reads it.
 *
 * The three sources used to travel as positional arguments through fourteen call
 * sites, and `toolUseParsed` was optional -- so a site that omitted it compiled and
 * silently lost the tool name on a result row, which is the one row that never
 * carries its own. Every field here is REQUIRED, so that omission is a type error.
 *
 * `toolName` is resolved ONCE, when the row is built. Three extractors used to
 * re-resolve it on the way down.
 */
export interface ZCodeRow {
  /** The row's own parsed content. */
  parsed: unknown
  /** Supplemental native records remain separate from the provider event. */
  supplemental: unknown
  /** The matching result can supply arguments that the scheduled event omitted. */
  result?: ParsedMessageContent
  /** The worker's record of the tool name, set on every span row. */
  spanType: string | undefined
  /** The paired scheduled request from the shared message resolver. */
  toolUseParsed: ParsedMessageContent | undefined
  /** The resolved tool name, or "" when no source states one. */
  toolName: string
}

/** Build a ZCodeRow from the three sources directly. */
export function zcodeRow(
  parsed: unknown,
  spanType: string | undefined,
  toolUseParsed: ParsedMessageContent | undefined,
  supplemental?: unknown,
): ZCodeRow {
  return { parsed, supplemental, spanType, toolUseParsed, toolName: resolveZCodeToolName(parsed, spanType, toolUseParsed) }
}

/**
 * Resolve the tool name for a row, across the payloads that omit it.
 *
 * Order matters. The payload's own name is authoritative when present (a
 * `scheduled` or `started`). The span type is the worker's record of the same name
 * and is set for every span row. The paired `scheduled` row is the last resort,
 * reached through the store's pre-parsed sibling.
 *
 * Private: every reader takes the resolved `row.toolName`, so this order is applied
 * in exactly one place.
 */
function resolveZCodeToolName(
  parsed: unknown,
  spanType: string | undefined,
  toolUseParsed: ParsedMessageContent | undefined,
): string {
  const own = zcodeExtractTool(parsed)?.toolName
  if (own)
    return own
  if (spanType)
    return spanType
  return zcodePairedRequest(parsed, toolUseParsed)?.toolName ?? ''
}

/** Accept only the scheduled request that belongs to the current tool call. */
export function zcodePairedRequest(parsed: unknown, request: ParsedMessageContent | undefined): ZCodeToolUpdate | null {
  const current = zcodeExtractTool(parsed)
  const paired = zcodeExtractTool(request?.parentObject)
  return current && paired?.kind === ZCODE_TOOL_KIND.Scheduled && paired.toolCallId === current.toolCallId ? paired : null
}

/**
 * The tool INPUT for a row, reaching back to the paired `scheduled` row when the
 * row itself carries none.
 *
 * A result row never carries the input, and the input is what a title needs (the
 * command that ran, the file that was read). The shared resolver combines the
 * original request with its supplemental stream arguments before this lookup.
 */
export function zcodeToolInput(row: ZCodeRow): Record<string, unknown> {
  const own = zcodeExtractTool(row.parsed)?.input
  if (own && Object.keys(own).length > 0)
    return own
  const request = zcodePairedRequest(row.parsed, row.toolUseParsed)?.input
  if (request && Object.keys(request).length > 0)
    return request
  const stored = pickObject(zcodeNativeTool(row)?.state, 'input')
  // EMPTY is not an answer, exactly as it is not one for the two lookups above. An
  // empty record short-circuited the result-row lookup that exists for this case, so
  // a Bash row drew `command: ''` and a Read row `path: ''` while the arguments sat
  // in the store the next lookup reads.
  if (stored && Object.keys(stored).length > 0)
    return stored
  const current = zcodeExtractTool(row.parsed)
  const result = zcodeExtractTool(row.result?.parentObject)
  if (current?.kind === ZCODE_TOOL_KIND.Scheduled && current.toolCallId
    && current.toolCallId === result?.toolCallId) {
    const completed = zcodeRow(row.result?.parentObject, row.spanType, row.toolUseParsed, row.result?.supplementalContent)
    return pickObject(zcodeNativeTool(completed)?.state, 'input') ?? {}
  }
  return {}
}

/**
 * The human-facing text of a failed tool call.
 *
 * The app-server states a failure two ways -- an `error` object with a `message`,
 * or a result whose `content` holds the text -- so both are read, error object
 * first.
 */
export function zcodeErrorText(update: ZCodeToolUpdate): string {
  if (update.error) {
    const message = pickString(update.error, 'message')
    if (message)
      return message
  }
  return update.result?.content ?? ''
}

/**
 * The to-do items a `TodoWrite` input carries.
 *
 * ZCode's input matches Claude Code's -- `{todos:[{content,status,activeForm}]}` --
 * and this reads ZCode's own copy of it rather than borrowing that provider's
 * extractor, so a divergence in either one stays local to the provider that made
 * it. Returns null when the input holds no `todos` array, which is what makes the
 * renderer fall back to the generic tool row instead of drawing an empty list.
 *
 * The ITEMS alone. The header above them belongs to `todoToolBody`, which every
 * provider's checklist shares, so the size and the cleared state read the same way
 * across providers.
 */
export function zcodeTodoItemsFromInput(input: Record<string, unknown> | null | undefined): TodoItem[] | null {
  return input && Array.isArray(input.todos) ? rawTodosToItems(input.todos) : null
}

/** Validate the native record against this event before resolving any of its fields. */
export function zcodeNativeTool(row: ZCodeRow): {
  sessionId: string
  messageId: string
  state: Record<string, unknown>
  artifacts: Record<string, unknown> | null
} | null {
  const original = zcodeExtractTool(row.parsed)
  const extra = zcodeEnvelope(row.supplemental)
  const { nativeTool: native, artifacts } = zcodeToolSupplement(row.supplemental)
  const data = pickObject(native, ZCODE_STORED_TOOL.Data)
  const state = pickObject(data, ZCODE_STORED_PART.State)
  const sessionId = pickString(native, ZCODE_STORED_TOOL.SessionID)
  const messageId = pickString(native, ZCODE_STORED_TOOL.MessageID)
  const ownPayload = zcodeEnvelope(row.parsed)?.payload
  const requestPayload = zcodePairedRequest(row.parsed, row.toolUseParsed)
    ? zcodeEnvelope(row.toolUseParsed?.parentObject)?.payload
    : undefined
  const agentId = pickString(ownPayload, 'agentId') || pickString(requestPayload, 'agentId')
  const childSessionId = pickString(ownPayload, 'childSessionId') || pickString(requestPayload, 'childSessionId')
  let nativeCallId = original?.toolCallId
  if (agentId) {
    const prefix = `${ZCODE_TOOL_PREFIX.Subagent}${agentId}_`
    if (!nativeCallId?.startsWith(prefix) || !childSessionId || childSessionId !== sessionId)
      return null
    nativeCallId = nativeCallId.slice(prefix.length)
  }
  if (!nativeCallId)
    return null
  if (!original || !original.toolCallId || extra?.type !== ZCODE_EVENT.ToolUpdated
    || extra.payload[ZCODE_SUPPLEMENT_PAYLOAD.Kind] !== original.kind
    || extra.payload[ZCODE_SUPPLEMENT_PAYLOAD.ToolCallID] !== original.toolCallId
    || data?.[ZCODE_STORED_PART.Type] !== ZCODE_STORED_PART_TYPE.Tool
    || data[ZCODE_STORED_PART.CallID] !== nativeCallId
    || !pickString(data, ZCODE_STORED_PART.Tool) || (row.toolName && data[ZCODE_STORED_PART.Tool] !== row.toolName)
    || !sessionId || !messageId || !state
    || state[ZCODE_STORED_PART.Status] !== (original.isError ? ZCODE_STORED_PART_STATUS.Error : ZCODE_STORED_PART_STATUS.Completed)) {
    return null
  }
  return { sessionId, messageId, state, artifacts: artifacts ?? null }
}

/**
 * The tool.updated kinds that OPEN a span rather than close it.
 *
 * `scheduled` is the request. `result`, `error`, and `batch` are final.
 * The Worker consumes `started` and `progress` for live counters.
 *
 * A RETAINED row is final whatever its kind. A turn that ends while the call runs
 * stores the agent's own last frame, which is a scheduled, started or progress kind,
 * and LeapMux's completion column is what states that the call did not finish. Every
 * Agent Client Protocol provider reads its retained rows the same way.
 *
 * A row that carries a tool-outcome note is final for the same reason: the agent sent
 * no result of its own, and the note is what LeapMux concluded instead.
 *
 * `parsed` is absent where a caller holds the row's bytes alone -- an isolated render
 * with no message store. The kind then decides, which is what the agent's own frames
 * say.
 */
export function zcodeToolSpanRole(kind: string, parsed: ParsedMessageContent | undefined): ToolSpanRole {
  if (retainedRowIsFinal(parsed?.completion) || parseToolOutcome(parsed?.messageMetadata) !== null)
    return 'result'
  if (kind === ZCODE_TOOL_KIND.Scheduled)
    return 'request'
  if (kind === ZCODE_TOOL_KIND.Result || kind === ZCODE_TOOL_KIND.Error || kind === ZCODE_TOOL_KIND.Batch)
    return 'result'
  return 'other'
}
