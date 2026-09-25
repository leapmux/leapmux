import type { ParsedMessageContent } from '~/lib/messageParser'
import { OH_MY_PI_EVENT, OH_MY_PI_FRAME_FIELD } from '~/generated/contracts/ohmypi-protocol'
import { asContentArray, splitToolResultContent } from '~/lib/contentBlocks'
import { pickObject, pickString } from '~/lib/jsonPick'

/**
 * The fields of one omp `tool_execution_*` frame. The three frames carry different
 * fields:
 *
 *   start  -> {toolCallId, toolName, args, intent?}
 *   update -> {toolCallId, toolName, args, partialResult}
 *   end    -> {toolCallId, toolName, result, isError}   // no args
 *
 * A row that reads an end frame takes the arguments from the paired start frame.
 */
export interface OhMyPiToolExecution {
  toolCallId: string
  toolName: string
  args: Record<string, unknown>
  /** The short reason the model gave for the call, when intent tracing is on. */
  intent: string
  result?: OhMyPiToolResult
  partialResult?: OhMyPiToolResult
  isError: boolean
}

/** One tool result: its text blocks joined, and its `details` record. */
export interface OhMyPiToolResult {
  text: string
  details: Record<string, unknown>
}

/**
 * The text blocks of one tool result, joined into paragraphs.
 *
 * Image blocks are left out. This text draws into a `<pre>` and reaches the clipboard,
 * and a data URL there is a megabyte of base64. `ohMyPiToolResultImages` draws the
 * images instead.
 */
export function ohMyPiToolResultText(result: Record<string, unknown> | null | undefined): string {
  return splitToolResultContent(asContentArray(result?.content), { text: 'text' }).text
}

function ohMyPiToolResult(result: Record<string, unknown>): OhMyPiToolResult {
  return {
    text: ohMyPiToolResultText(result),
    details: pickObject(result, OH_MY_PI_FRAME_FIELD.Details) ?? {},
  }
}

// Memoized by payload identity: one row build reads the same frame through the kind
// dispatch and again through each kind reader, and the content blocks are walked once.
const toolCache = new WeakMap<Record<string, unknown>, OhMyPiToolExecution | null>()

/** One `tool_execution_*` frame read into {@link OhMyPiToolExecution}, or null for another frame. */
export function ohMyPiExtractTool(payload: Record<string, unknown> | null | undefined): OhMyPiToolExecution | null {
  if (!payload)
    return null
  const cached = toolCache.get(payload)
  if (cached !== undefined)
    return cached
  const toolCallId = pickString(payload, OH_MY_PI_FRAME_FIELD.ToolCallID)
  const toolName = pickString(payload, OH_MY_PI_FRAME_FIELD.ToolName)
  if (!toolCallId || !toolName) {
    toolCache.set(payload, null)
    return null
  }
  const result = pickObject(payload, OH_MY_PI_FRAME_FIELD.Result)
  const partial = pickObject(payload, OH_MY_PI_FRAME_FIELD.PartialResult)
  const tool: OhMyPiToolExecution = {
    toolCallId,
    toolName,
    args: pickObject(payload, OH_MY_PI_FRAME_FIELD.Args) ?? {},
    intent: pickString(payload, 'intent'),
    ...(result ? { result: ohMyPiToolResult(result) } : {}),
    ...(partial ? { partialResult: ohMyPiToolResult(partial) } : {}),
    isError: payload.isError === true,
  }
  toolCache.set(payload, tool)
  return tool
}

/** The frame's `type`, when it is one of the three tool frames. */
function toolFrameType(payload: Record<string, unknown> | null | undefined): string {
  const type = pickString(payload, 'type')
  return type === OH_MY_PI_EVENT.ToolExecutionStart || type === OH_MY_PI_EVENT.ToolExecutionEnd ? type : ''
}

/**
 * The paired start frame, when it is the start of THIS call: the same call id and the
 * same tool. A frame of another call is no side of this one, since one turn can run
 * several calls at once.
 */
export function ohMyPiPairedRequest(payload: Record<string, unknown> | null | undefined, request?: ParsedMessageContent): ParsedMessageContent | undefined {
  const current = ohMyPiExtractTool(payload)
  const paired = ohMyPiExtractTool(request?.parentObject)
  return current && paired && toolFrameType(request?.parentObject) === OH_MY_PI_EVENT.ToolExecutionStart
    && paired.toolCallId === current.toolCallId && paired.toolName === current.toolName
    ? request
    : undefined
}

/** The paired end frame, when it is the end of THIS call. */
export function ohMyPiPairedResult(payload: Record<string, unknown> | null | undefined, result?: ParsedMessageContent): ParsedMessageContent | undefined {
  const current = ohMyPiExtractTool(payload)
  const paired = ohMyPiExtractTool(result?.parentObject)
  return current && paired && toolFrameType(result?.parentObject) === OH_MY_PI_EVENT.ToolExecutionEnd
    && paired.toolCallId === current.toolCallId && paired.toolName === current.toolName
    ? result
    : undefined
}
