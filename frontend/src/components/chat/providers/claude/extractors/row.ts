import type { ChatRow } from '../../../model/row'
import type { ClaudeRowContext, ClaudeToolRow } from './toolCommon'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { joinContentParagraphs } from '~/lib/contentBlocks'
import { pickObject } from '~/lib/jsonPick'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { toolCallRow } from '../../../model/row'
import { retainedRowIsFinal } from '../../registry'
import { canonicalClaudeToolName, claudeToolRowHidden } from '../toolKinds'
import { getMessageContentArray } from './assistantContent'
import { claudePlanFromEnvelope } from './plan'
import { claudeToolCall } from './toolCall'
import { claudeToolRow } from './toolCommon'

const LOCAL_COMMAND_OPEN = '<local-command-stdout>'
const LOCAL_COMMAND_CLOSE = '</local-command-stdout>'

/**
 * Read one Claude row into the shared row model.
 *
 * Claude states every row inside an Anthropic envelope, so the work here is two
 * steps: find which of the envelope's content blocks carries the row, and turn
 * it into the neutral shape. The tool table itself lives in
 * `../toolPresentation.ts`, beside the identity tables it reads.
 */
export function claudeExtractRow(input: RowExtractionInput): ChatRow | null {
  const { category, resolved: parsed } = input
  const payload = parsed.parentObject
  switch (category.kind) {
    case 'tool_use':
    case 'tool_result':
      return claudeToolSpanRow(input)
    case 'assistant_text':
      return claudeAssistantRow(payload, 'text')
    case 'assistant_thinking':
      return claudeAssistantRow(payload, 'thinking')
    case 'assistant_plan': {
      // The same reader the classifier used, so the two layers state one answer.
      const plan = claudePlanFromEnvelope(payload)
      return plan
        ? { kind: 'assistant-plan', text: plan }
        : null
    }
    case 'user_content':
      return leapmuxUserRow(payload)
    case 'plan_execution':
      return leapmuxPlanExecutionRow(payload)
    case 'unknown':
      return claudeUnknownRow(payload)
    default:
      return null
  }
}

/**
 * The assistant text or thinking a Claude envelope carries.
 *
 * An envelope with the block but no words in it is a row with nothing to show,
 * not one this provider failed to read. A signature-only thinking block reaches
 * that state, and the classifier already hides it.
 */
function claudeAssistantRow(payload: Record<string, unknown> | undefined, block: 'text' | 'thinking'): ChatRow {
  const content = getMessageContentArray(payload)
  const text = content
    ? joinContentParagraphs(content, { [block]: block })
    : ''
  if (!text)
    return { kind: 'hidden' }
  return block === 'text'
    ? { kind: 'assistant-text', text }
    : { kind: 'assistant-thinking', text }
}

/**
 * A Claude transcript row whose body is the user's own words.
 *
 * Two shapes reach here. A local slash command (`/context`) answers with a
 * string, sometimes wrapped in a `<local-command-stdout>` pair that is markup
 * for the model rather than for a reader. A message forwarded into a SUBAGENT's
 * transcript answers with a block array instead.
 */
function claudeUserTextRow(payload: Record<string, unknown> | undefined): ChatRow | null {
  const message = pickObject(payload, 'message')
  if (!message)
    return null
  const raw = message.content
  if (Array.isArray(raw)) {
    const text = joinContentParagraphs(raw, { text: 'text' })
    return text
      ? { kind: 'user', text, attachments: [] }
      : { kind: 'hidden' }
  }
  if (typeof raw !== 'string')
    return null
  const open = raw.indexOf(LOCAL_COMMAND_OPEN)
  const close = raw.indexOf(LOCAL_COMMAND_CLOSE)
  const text = open !== -1 && close > open
    ? raw.slice(open + LOCAL_COMMAND_OPEN.length, close).trim()
    : raw
  return text
    ? { kind: 'user', text, attachments: [] }
    : { kind: 'hidden' }
}

/**
 * A row the classifier could not identify, matched by its shape instead.
 *
 * The order is the one the renderer chain used: the user shapes are tested
 * before the assistant ones, because a `{type:'user'}` envelope carries a
 * `message.content` array that reads as an assistant block list.
 */
function claudeUnknownRow(payload: Record<string, unknown> | undefined): ChatRow | null {
  if (!payload)
    return null
  // Each step falls through on `hidden` as well as on null: the shape matched but
  // held no words, and a later step may still find some. A row that every step
  // declines returns null, and the reader gets the frame in the shared card.
  if (payload.type === 'user') {
    const user = claudeUserTextRow(payload)
    if (user && user.kind !== 'hidden')
      return user
  }
  // The envelope `type` is NOT tested here. A row the classifier could not identify
  // often carries no type at all, and its `message.content[]` is still the
  // Anthropic block array that holds the words.
  const text = claudeAssistantRow(payload, 'text')
  if (text.kind !== 'hidden')
    return text
  const thinking = claudeAssistantRow(payload, 'thinking')
  if (thinking.kind !== 'hidden')
    return thinking
  // The LeapMux-shaped user send, which carries no envelope `type` at all.
  return 'type' in payload
    ? null
    : leapmuxUserRow(payload)
}

/**
 * Resolve the sides of one Claude tool span into ONE call, plus where this row
 * sits in the span.
 *
 * A PLAN never reaches here: `ExitPlanMode` proposes one in its arguments rather
 * than returning a tool result, and `classify` already routed that frame to
 * `assistant_plan` -- see {@link claudePlanFromEnvelope}. An `ExitPlanMode` call
 * that carried NO plan does reach here, and draws the ordinary tool row.
 */
function claudeToolSpanRow(input: RowExtractionInput): ChatRow | null {
  const { resolved: parsed, span, spanType, completion } = input
  const own = claudeToolRow(parsed, spanType, span)
  if (!own)
    return null
  if (claudeToolRowHidden(own.toolName, own.role))
    return { kind: 'hidden' }

  // The span's tool name is ONE fact, and either side may be the one that knows
  // it: a result row takes its tool name from the span column, and a row rendered
  // without one would otherwise report no tool at all.
  // Canonicalized as the row's own name is, because the worker's `span_type` column
  // carries the RAW name: the request side then reads the same tool as this row.
  const spanTool = canonicalClaudeToolName(spanType || own.toolName)
  // A sibling belongs to THIS call only when its tool-use id says so: one turn
  // can carry several parallel calls, and a request with another id is not ours.
  const sideRow = (side: ParsedMessageContent | undefined): ClaudeToolRow | null => {
    const row = side
      ? claudeToolRow(side, spanTool, span)
      : null
    return row && row.id === own.id
      ? row
      : null
  }
  // A RESULT row carries no arguments of its own, so the call reads them from the
  // request of its span. A request from ANOTHER call is no side of this one at all.
  const requestSide = sideRow(span.request)
  const argsRow = requestSide ?? own
  // Only a result row holds a result. A request row never reads its result: it shows
  // and reports its height before the result exists (see ToolSpanContext).
  const resultRow = own.role === 'result'
    ? own
    : undefined
  const context: ClaudeRowContext = {
    // The worker stores a TaskUpdate's post-update snapshot on the RESULT message, so
    // only the result row carries it, and only the result row can miss it.
    ...(resultRow !== undefined && input.todoSnapshot !== undefined
      ? { todoSnapshot: input.todoSnapshot }
      : {}),
    ...(resultRow !== undefined && input.todoSnapshotDiagnostic !== undefined
      ? { todoSnapshotDiagnostic: input.todoSnapshotDiagnostic }
      : {}),
    ...(completion !== undefined
      ? { completion }
      : {}),
  }
  const call = claudeToolCall(argsRow, resultRow, context)
  // A turn that ended while the call ran stores the `tool_use` frame AGAIN as the closing
  // row: that copy holds no result, and it is the span's result side, which states how
  // the call ended (see claudeSpanRole).
  const rowRole = own.role === 'request' && retainedRowIsFinal(completion)
    ? 'result'
    : own.role
  // A request of another call is no request row of this one. The transcript draws no
  // request row for a request that it hides (see createRequestRowPresence in
  // chatEntryCache), so this row then draws the request side itself.
  return toolCallRow(call, rowRole, {
    request: span.visibleRows.request && !!requestSide,
  })
}
