import type { RenderContext } from '../../../messageRenderers'
import type { TodoListSource } from '../../../todoListMessage'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { ZCODE_EVENT, ZCODE_TOOL, ZCODE_TOOL_KIND } from '~/generated/contracts/zcode-protocol'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { pluralize } from '~/lib/plural'
import { rawTodosToItems } from '~/stores/chatTodos'

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
 * The event envelope LeapMux persists. Every conversation row is one of these, so
 * the extractors unwrap it once rather than each reading `payload` by hand.
 */
export interface ZCodeEnvelope {
  type: string
  payload: Record<string, unknown>
}

/** Unwrap a persisted ZCode row into its event type and payload. */
export function zcodeEnvelope(parsed: unknown): ZCodeEnvelope | null {
  if (!isObject(parsed))
    return null
  const type = pickString(parsed, 'type')
  if (!type)
    return null
  return { type, payload: pickObject(parsed, 'payload') ?? {} }
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

// Memoized by payload identity: `zcodeToolResultMeta`, the result-body renderer and
// the per-tool extractors each unwrap the same row once per render, and the content
// walk is not free. WeakMap-keyed so an entry is collected with its payload.
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
  const result = rawResult ? zcodeToolResult(rawResult) : null
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
  /** The worker's record of the tool name, set on every span row. */
  spanType: string | undefined
  /** The paired `scheduled` row, pre-parsed by the store. */
  toolUseParsed: ParsedMessageContent | undefined
  /** The resolved tool name, or "" when no source states one. */
  toolName: string
}

/**
 * Build a ZCodeRow from a renderer's props.
 *
 * Call it inside the memo that already tracks `props`, so Solid's reactivity is
 * unchanged: the row is derived state, not a value read outside a tracking scope.
 */
export function zcodeRowFrom(props: { parsed: unknown, context?: RenderContext }): ZCodeRow {
  return zcodeRow(props.parsed, props.context?.spanType, props.context?.toolUseParsed)
}

/** Build a ZCodeRow from the three sources directly, for a caller that holds no props. */
export function zcodeRow(
  parsed: unknown,
  spanType: string | undefined,
  toolUseParsed: ParsedMessageContent | undefined,
): ZCodeRow {
  return { parsed, spanType, toolUseParsed, toolName: resolveZCodeToolName(parsed, spanType, toolUseParsed) }
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
  return zcodeExtractTool(toolUseParsed?.parentObject)?.toolName ?? ''
}

/**
 * The tool INPUT for a row, reaching back to the paired `scheduled` row when the
 * row itself carries none.
 *
 * A result row never carries the input, and the input is what a title needs (the
 * command that ran, the file that was read). The worker backfills the scheduled
 * row's input from the model stream, so the sibling is the reliable copy.
 */
export function zcodeToolInput(row: ZCodeRow): Record<string, unknown> {
  const own = zcodeExtractTool(row.parsed)?.input
  if (own && Object.keys(own).length > 0)
    return own
  return zcodeExtractTool(row.toolUseParsed?.parentObject)?.input ?? {}
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
 * The to-do list a `TodoWrite` input carries, in the shared checklist shape.
 *
 * ZCode's input matches Claude Code's -- `{todos:[{content,status,activeForm}]}` --
 * and this reads ZCode's own copy of it rather than borrowing that provider's
 * extractor, so a divergence in either one stays local to the provider that made
 * it. Returns null when the input holds no `todos` array, which is what makes the
 * renderer fall back to the generic tool row instead of drawing an empty list.
 */
export function zcodeTodoListFromInput(input: Record<string, unknown> | null | undefined): TodoListSource | null {
  if (!input || !Array.isArray(input.todos))
    return null
  const todos = rawTodosToItems(input.todos)
  return {
    toolName: ZCODE_TOOL.TodoWrite,
    title: pluralize(todos.length, 'task'),
    todos,
  }
}
