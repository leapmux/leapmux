import type { ChatRow } from '../../../model/row'
import type { ToolCallLifecycleFacts } from '../../../model/toolCall'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import { LETTA_DELTA_FIELD, LETTA_TOOL_STATUS } from '~/generated/contracts/letta-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { leapmuxUserRow } from '../../../leapmuxRows'
import { createToolCall } from '../../../model/createToolCall'
import { toolCallRow } from '../../../model/row'
import { failedResult } from '../../../model/toolCall'
import { retainedOutcome } from '../../registry'
import { lettaToolKind } from '../toolKinds'
import { isLettaToolProgress, lettaReturnedData, lettaToolPayload } from '../toolOutput'

/**
 * Read one Letta Code row into the shared row model.
 */
export function lettaExtractRow(input: RowExtractionInput): ChatRow | null {
  const { category, resolved: parsed } = input
  const payload = parsed.parentObject
  switch (category.kind) {
    case 'assistant_text':
      return { kind: 'assistant-text', text: pickString(payload, 'text') || '' }
    case 'assistant_thinking':
      return { kind: 'assistant-thinking', text: pickString(payload, 'text') || '' }
    case 'tool_use':
    case 'tool_result':
      return lettaToolRow(input, category.kind === 'tool_result')
    case 'user_content':
      return leapmuxUserRow(payload)
    case 'result_divider':
      return { kind: 'divider', divider: { label: 'Turn ended' } }
    default:
      return null
  }
}

/** The lifecycle every Letta tool row shares. */
function lettaLifecycle(
  completion: RowExtractionInput['resolved']['completion'],
  isResult: boolean,
  nativeStatus: string | undefined,
  hasResult: boolean,
): ToolCallLifecycleFacts {
  return {
    frameStatus: 'unstated',
    providerOutcome: nativeStatus === LETTA_TOOL_STATUS.Error ? 'failed' : nativeStatus === LETTA_TOOL_STATUS.Success ? 'succeeded' : null,
    retainedOutcome: retainedOutcome(completion),
    rowFinal: isResult,
    resultFrameLanded: hasResult,
  }
}

function lettaCallId(source: Record<string, unknown> | null): string | undefined {
  return pickString(source, LETTA_DELTA_FIELD.ToolCallID)
    || pickString(lettaNativeToolCall(source), LETTA_DELTA_FIELD.ToolCallID)
}

function lettaArguments(source: Record<string, unknown> | null): Record<string, unknown> | null {
  return lettaObjectArguments(source?.[LETTA_DELTA_FIELD.ToolArgs])
    ?? lettaObjectArguments(source?.[LETTA_DELTA_FIELD.ToolInput])
    ?? lettaObjectArguments(lettaNativeToolCall(source)?.[LETTA_DELTA_FIELD.Arguments])
}

/** The row one tool call becomes, with both span sides resolved. */
function lettaToolRow(
  input: RowExtractionInput,
  isResult: boolean,
): ChatRow | null {
  const { resolved: parsed, span } = input
  const payload = parsed.parentObject
  const completion = input.completion ?? parsed.completion
  const source = lettaToolPayload(payload)
  if (!source)
    return null
  // The Worker stores one native call per row. Raw frames can wrap that payload.
  // A paired request supplies arguments only when its native call ID matches.
  const id = lettaCallId(source) ?? ''
  const candidateRequest = lettaToolPayload(span.request?.parentObject)
  const requestSource = lettaMatchingCall(source, candidateRequest) ? candidateRequest : null
  const nativeCall = lettaNativeToolCall(source)
  const requestCall = lettaNativeToolCall(requestSource)
  const name = pickString(source, LETTA_DELTA_FIELD.ToolName)
    || pickString(nativeCall, LETTA_DELTA_FIELD.Name)
    || pickString(requestSource, LETTA_DELTA_FIELD.ToolName)
    || pickString(requestCall, LETTA_DELTA_FIELD.Name)
    || 'Tool'
  const args = lettaArguments(source) ?? lettaArguments(requestSource) ?? {}
  const candidateResult = isResult ? source : lettaToolPayload(span.result?.parentObject)
  const resultSource = lettaMatchingCall(source, candidateResult) && !isLettaToolProgress(candidateResult) ? candidateResult : null
  const returned = lettaReturnedData(resultSource)
  const nativeText = returned.kind === 'present'
    ? (typeof returned.value === 'string' ? returned.value : JSON.stringify(returned.value))
    : undefined
  const resultText = nativeText
  const declared = lettaToolKind(name)
  const kind = declared === 'unspecified' ? 'other' : declared
  const nativeStatus = pickString(resultSource, LETTA_DELTA_FIELD.Status, undefined) ?? (returned.kind === 'present' ? returned.status : undefined)
  const lifecycle = lettaLifecycle(completion, isResult, nativeStatus, resultText !== undefined)
  const envelope = { id, name, lifecycle }
  const hasInput = Object.keys(args).length > 0
  const interrupted = lifecycle.retainedOutcome === 'interrupted'
  const failed = nativeStatus === LETTA_TOOL_STATUS.Error || lifecycle.retainedOutcome === 'failed'
  // The arguments describe requested changes. Only native success proves applied changes.
  // Empty replacements remain present, because they mean deletion rather than absent input.
  const filePath = pickString(args, 'path', undefined) ?? pickString(args, 'file_path', undefined)
  const oldStr = pickString(args, 'old_string', undefined) ?? pickString(args, 'old_str', undefined)
  const newStr = pickString(args, 'new_string', undefined) ?? pickString(args, 'new_str', undefined) ?? pickString(args, 'content', undefined)
  const fileInputComplete = filePath !== undefined && filePath.trim() !== '' && newStr !== undefined && (kind === 'write' || oldStr !== undefined)
  if ((kind === 'edit' || kind === 'write') && fileInputComplete) {
    const change = {
      filePath,
      operation: kind === 'write' ? 'add' as const : 'edit' as const,
      oldStr: kind === 'write' ? '' : oldStr ?? '',
      newStr,
    }
    const call = createToolCall(envelope, {
      kind,
      name,
      request: { changes: [change] },
      ...(failed && resultText !== undefined
        ? { result: failedResult(resultText) }
        : !interrupted && nativeStatus === LETTA_TOOL_STATUS.Success && resultText !== undefined ? { result: { changes: [change] } } : {}),
    })
    return toolCallRow(call, isResult ? 'result' : 'request', span.visibleRows)
  }
  // Other tools keep their generic result card. Native status remains independent of its text.
  const call = createToolCall(envelope, {
    kind: hasInput && !isResult && kind !== 'edit' && kind !== 'write' ? kind : 'other',
    name,
    request: hasInput && !isResult ? args : { args },
    ...(resultText !== undefined
      ? { result: failed ? failedResult(resultText) : { content: [{ type: 'text' as const, text: resultText }] } }
      : {}),
  })
  return toolCallRow(call, isResult ? 'result' : 'request', span.visibleRows)
}

function lettaMatchingCall(source: Record<string, unknown>, candidate: Record<string, unknown> | null): boolean {
  const id = lettaCallId(source)
  if (!id || lettaCallId(candidate) !== id)
    return false
  return pickString(source, LETTA_DELTA_FIELD.RunID, undefined) === pickString(candidate, LETTA_DELTA_FIELD.RunID, undefined)
}

function lettaNativeToolCall(source: Record<string, unknown> | null | undefined): Record<string, unknown> | null {
  const singular = pickObject(source, LETTA_DELTA_FIELD.ToolCall)
  if (singular)
    return singular
  const calls = source?.[LETTA_DELTA_FIELD.ToolCalls]
  return Array.isArray(calls) ? (calls.find(isObject) ?? null) : null
}

function lettaObjectArguments(raw: unknown): Record<string, unknown> | null {
  if (isObject(raw))
    return raw
  if (typeof raw !== 'string')
    return null
  try {
    const parsed: unknown = JSON.parse(raw)
    return isObject(parsed) ? parsed : null
  }
  catch {
    return null
  }
}
