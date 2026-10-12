import type { CommandResult } from '../../../model/commandResult'
import type { ChatRow } from '../../../model/row'
import type { ToolCall, ToolCallEnvelope, ToolCallLifecycleFacts } from '../../../model/toolCall'
import type { ToolKind } from '../../../model/toolKind'
import type { ToolRequestByKind } from '../../../model/tools'
import type { ToolRequestOverrides } from '../../defaultToolRequests'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import { LETTA_DELTA_FIELD, LETTA_TOOL_STATUS } from '~/generated/contracts/letta-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { leapmuxUserRow } from '../../../leapmuxRows'
import { createToolCall } from '../../../model/createToolCall'
import { toolCallRow } from '../../../model/row'
import { failedResult, isGenericKind, unparsedResult } from '../../../model/toolCall'
import { toolRequestFor } from '../../defaultToolRequests'
import { retainedOutcome } from '../../registry'
import { lettaQuestionReceiptMessage, lettaQuestionsFromToolInput } from '../askUserQuestion'
import { lettaToolKind } from '../toolKinds'
import { isLettaToolProgress, lettaReturnedData, lettaToolPayload } from '../toolOutput'
import { lettaTodoLists } from './todo'

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
    // `extractDivider` reads the turn end. The shared extraction never passes a
    // `result_divider` row here.
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
  return lettaJsonObject(source?.[LETTA_DELTA_FIELD.ToolArgs])
    ?? lettaJsonObject(source?.[LETTA_DELTA_FIELD.ToolInput])
    ?? lettaJsonObject(lettaNativeToolCall(source)?.[LETTA_DELTA_FIELD.Arguments])
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
  const declared = lettaToolKind(name)
  const kind = declared === 'unspecified' ? 'other' : declared
  const resultText = returned.kind === 'present' ? lettaResultText(kind, returned.value) : undefined
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
  const facts: LettaToolFacts = { envelope, name, args, resultText, failed }
  // A task call draws its checklist on both rows. The native answer is the saved
  // task record, so the result row can read it without the paired request.
  if (kind === 'todo') {
    const lists = lettaTodoLists(name, args, returned.kind === 'present' && !failed ? returned.value : undefined)
    if (lists) {
      const call = createToolCall(envelope, {
        kind: 'todo',
        name,
        request: lists.request,
        ...(resultText !== undefined ? { result: failed ? failedResult(resultText) : lists.result ?? unparsedResult(resultText) } : {}),
      })
      return toolCallRow(call, isResult ? 'result' : 'request', span.visibleRows)
    }
  }
  // A shell call states its command, so its result row draws the command result: the
  // output, and the code of a command that failed.
  if (kind === 'execute' && hasInput && resultText !== undefined)
    return toolCallRow(lettaShellCall({ ...facts, resultText }), isResult ? 'result' : 'request', span.visibleRows)
  // A result row, a call that states no input and a call whose declared kind has
  // no reading here keep the generic result card. Native status remains
  // independent of its text.
  const call = isResult || !hasInput || kind === 'todo' || kind === 'edit' || kind === 'write' || isGenericKind(kind)
    ? lettaGenericCall(facts)
    : lettaDeclaredCall(kind, facts)
  return toolCallRow(call, isResult ? 'result' : 'request', span.visibleRows)
}

/**
 * The returned data of one call as the text that its result card draws.
 *
 * A question call returns a receipt at once, and the card draws the sentence that
 * the receipt states for the reader, not the JSON that holds it. Every other
 * return stays as Letta Code wrote it.
 */
function lettaResultText(kind: ToolKind, value: unknown): string {
  const text = typeof value === 'string' ? value : JSON.stringify(value)
  return (kind === 'question' ? lettaQuestionReceiptMessage(lettaJsonObject(value)) : undefined) ?? text
}

/** Everything one tool row states, collected once. */
interface LettaToolFacts {
  envelope: ToolCallEnvelope
  name: string
  args: Record<string, unknown>
  /** The returned data as text. Undefined when no matching answer landed. */
  resultText: string | undefined
  failed: boolean
}

/** The generic card: the native arguments and the native text. */
function lettaGenericCall(facts: LettaToolFacts): ToolCall {
  return createToolCall(facts.envelope, {
    kind: 'other',
    name: facts.name,
    request: { args: facts.args },
    ...(facts.resultText !== undefined
      ? { result: facts.failed ? failedResult(facts.resultText) : { content: [{ type: 'text' as const, text: facts.resultText }] } }
      : {}),
  })
}

/**
 * The kinds whose Letta arguments the shared table cannot read.
 *
 * Every other declared kind reads the shared table: Letta spells `command`,
 * `file_path`, `description` and `prompt` as the table does. `todo` stays out of
 * this table, because its request needs the native answer as well as the
 * arguments; {@link lettaTodoLists} reads it.
 */
export const LETTA_TOOL_REQUEST_OVERRIDES: ToolRequestOverrides<null> = {
  // The questions and their options, whose shape is Letta's own.
  question: (args): ToolRequestByKind['question'] => ({ questions: lettaQuestionsFromToolInput(args) }),
}

/**
 * A call of a declared kind: the request that its arguments state, read through
 * the shared table, and its answer as plain text.
 *
 * The kind renderers read their own request and result fields without a guard.
 * The raw arguments and a generic `{content}` result therefore cannot stand in
 * for them: a `todo` row with the raw `TaskCreate` arguments read
 * `request.items` as undefined and took the whole page into the ErrorBoundary.
 */
function lettaDeclaredCall<K extends ToolKind>(kind: K, facts: LettaToolFacts): ToolCall {
  return createToolCall(facts.envelope, {
    kind,
    name: facts.name,
    request: toolRequestFor(kind, facts.args, null, LETTA_TOOL_REQUEST_OVERRIDES),
    ...(facts.resultText !== undefined ? { result: facts.failed ? failedResult(facts.resultText) : unparsedResult(facts.resultText) } : {}),
  })
}

/**
 * The line that Letta Code 0.34.2 writes before the output of a failed foreground
 * command (`bash` in the CLI bundle): the detail `Exit code: N`. A recovery note can
 * stand before it.
 */
const LETTA_EXIT_LINE = /^((?:Note: [^\n]*\n)?)Exit code: (-?\d+)(?:\n|$)/

/**
 * One shell call: the request its arguments state, and its output with the code of a
 * failed command.
 *
 * Only a call that the native status marks as failed reads the line: a successful
 * command can print the same words, and they are its output. A failure detail that
 * states no code, such as a signal, stays in the output, where the reader sees why.
 */
function lettaShellCall(facts: LettaToolFacts & { resultText: string }): ToolCall {
  const exitLine = facts.failed ? LETTA_EXIT_LINE.exec(facts.resultText) : null
  const exitCode = exitLine ? Number(exitLine[2]) : Number.NaN
  const command: CommandResult = exitLine && Number.isSafeInteger(exitCode)
    ? { output: `${exitLine[1] ?? ''}${facts.resultText.slice(exitLine[0].length)}`, exitCode }
    : { output: facts.resultText }
  return createToolCall(facts.envelope, {
    kind: 'execute',
    name: facts.name,
    request: toolRequestFor('execute', facts.args, null, LETTA_TOOL_REQUEST_OVERRIDES),
    result: { commands: [command], unresolvedTerminals: [] },
  })
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

/** The object that `raw` is, or that the JSON text `raw` states. Anything else is null. */
function lettaJsonObject(raw: unknown): Record<string, unknown> | null {
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
