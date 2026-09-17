import type { ChatRowIR } from '../../../ir/row'
import type { ClaudeRowContext, ClaudeToolRow } from './toolCommon'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { joinContentParagraphs } from '~/lib/contentBlocks'
import { pickObject, pickString } from '~/lib/jsonPick'
import { toolCallRow } from '../../../ir/row'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { canonicalClaudeToolName, claudeToolRowHidden } from '../toolKinds'
import { getMessageContentArray } from './assistantContent'
import { claudePlanFromEnvelope } from './plan'
import { claudeTaskGetUnresolved } from './todo'
import { claudeToolCallIR } from './toolCall'
import { claudeToolRow } from './toolCommon'

const LOCAL_COMMAND_OPEN = '<local-command-stdout>'
const LOCAL_COMMAND_CLOSE = '</local-command-stdout>'

/**
 * Read one Claude row into the shared row IR.
 *
 * Claude states every row inside an Anthropic envelope, so the work here is two
 * steps: find which of the envelope's content blocks carries the row, and turn
 * it into the neutral shape. The tool table itself lives in
 * `../toolPresentation.ts`, beside the identity tables it reads.
 */
export function claudeExtractRow(input: RowExtractionInput): ChatRowIR | null {
  const { category, parsed } = input
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
      return plan ? { kind: 'assistant-plan', text: plan } : null
    }
    case 'agent_prompt':
      return claudeAgentPromptRow(payload)
    case 'user_text':
      return claudeUserTextRow(payload)
    case 'user_content':
      return leapmuxUserRow(payload)
    case 'plan_execution':
      return leapmuxPlanExecutionRow(payload)
    case 'compact_summary':
      return claudeCompactSummaryRow(payload)
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
function claudeAssistantRow(payload: Record<string, unknown> | undefined, block: 'text' | 'thinking'): ChatRowIR {
  const content = getMessageContentArray(payload)
  const text = content ? joinContentParagraphs(content, { [block]: block }) : ''
  if (!text)
    return { kind: 'hidden' }
  return block === 'text' ? { kind: 'assistant-text', text } : { kind: 'assistant-thinking', text }
}

/**
 * A Claude transcript row whose body is the user's own words.
 *
 * Two shapes reach here. A local slash command (`/context`) answers with a
 * string, sometimes wrapped in a `<local-command-stdout>` pair that is markup
 * for the model rather than for a reader. A message forwarded into a SUBAGENT's
 * transcript answers with a block array instead.
 */
function claudeUserTextRow(payload: Record<string, unknown> | undefined): ChatRowIR | null {
  const message = pickObject(payload, 'message')
  if (!message)
    return null
  const raw = message.content
  if (Array.isArray(raw)) {
    const text = joinContentParagraphs(raw, { text: 'text' })
    return text ? { kind: 'user', text, attachments: [] } : { kind: 'hidden' }
  }
  if (typeof raw !== 'string')
    return null
  const open = raw.indexOf(LOCAL_COMMAND_OPEN)
  const close = raw.indexOf(LOCAL_COMMAND_CLOSE)
  const text = open !== -1 && close > open
    ? raw.slice(open + LOCAL_COMMAND_OPEN.length, close).trim()
    : raw
  return text ? { kind: 'user', text, attachments: [] } : { kind: 'hidden' }
}

/** The instruction a parent sent to one of its subagents. */
function claudeAgentPromptRow(payload: Record<string, unknown> | undefined): ChatRowIR | null {
  if (!payload || payload.type !== 'user' || typeof payload.parent_tool_use_id !== 'string')
    return null
  const content = pickObject(payload, 'message')?.content
  if (!Array.isArray(content))
    return null
  const prompt = joinContentParagraphs(content, { text: 'text' })
  return prompt ? { kind: 'agent-prompt', prompt: { prompt } } : null
}

/**
 * The summary the command line interface wrote when it rewrote its own context.
 *
 * The boundary row beside it states the token transition; this states WHAT
 * survived, which nothing else in the transcript does. It used to draw an empty
 * bubble.
 */
function claudeCompactSummaryRow(payload: Record<string, unknown> | undefined): ChatRowIR {
  const content = getMessageContentArray(payload)
  const summary = content ? joinContentParagraphs(content, { text: 'text' }) : pickString(payload, 'content')
  if (!summary)
    return { kind: 'hidden' }
  return { kind: 'compact-summary', summary }
}

/**
 * A row the classifier could not identify, matched by its shape instead.
 *
 * The order is the one the renderer chain used: the user shapes are tested
 * before the assistant ones, because a `{type:'user'}` envelope carries a
 * `message.content` array that reads as an assistant block list.
 */
function claudeUnknownRow(payload: Record<string, unknown> | undefined): ChatRowIR | null {
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
  return 'type' in payload ? null : leapmuxUserRow(payload)
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
function claudeToolSpanRow(input: RowExtractionInput): ChatRowIR | null {
  const { parsed, sides, spanType, completion } = input
  const own = claudeToolRow(parsed, spanType, sides)
  if (!own)
    return null
  if (claudeToolRowHidden(own.toolName, own.role))
    return { kind: 'hidden' }

  // A RESULT row carries no arguments of its own, so the call reads the request
  // beside it. When this row IS that request, it stands in for itself: without
  // that, a result rendered before the store resolved the pair lost its subagent
  // card, its diff and its file path all at once.
  const spanSides = sides.request ? sides : { ...sides, request: own.role === 'request' ? parsed : undefined }
  // The span's tool name is ONE fact, and either side may be the one that knows
  // it: a result row takes its tool name from the span column, and a row rendered
  // without one would otherwise report no tool at all.
  // Canonicalized, because `claudeToolRowHidden` states "canonical names only"
  // and the worker's `span_type` column carries the RAW name. No alias maps onto a
  // hidden tool today, but one added later would make the two sides of a span
  // disagree about whether the other draws, and the pair would render blank.
  const spanTool = canonicalClaudeToolName(spanType || own.toolName)
  // A sibling belongs to THIS call only when its tool-use id says so: one turn
  // can carry several parallel calls, and a request with another id is not ours.
  const sideRow = (side: ParsedMessageContent | undefined): ClaudeToolRow | null => {
    const row = side ? claudeToolRow(side, spanTool, spanSides) : null
    return row && row.id === own.id ? row : null
  }
  // A side whose row is HIDDEN draws nothing beside this one, so it must not
  // suppress this row's own header or request body -- and a side from ANOTHER
  // call is no side of this one at all.
  const requestSide = sideRow(spanSides.request)
  const resultSide = sideRow(sides.result)
  const argsRow = requestSide ?? own
  const resultRow = resultSide ?? (own.role === 'result' ? own : undefined)
  const context: ClaudeRowContext = {
    // The MATCHED side, not `sides.result`: the rule `sideRow` states applies here
    // too, and reading the raw side skipped the tool-use id test. `claudeToolRow`
    // also declines a block whose id is empty, which the raw read admitted. Each
    // optional half rides only when this row carries it.
    ...(resultSide?.toolUseResult !== undefined ? { pairedResult: resultSide.toolUseResult } : {}),
    ...(input.todoById !== undefined ? { todoById: input.todoById } : {}),
    ...(completion !== undefined ? { completion } : {}),
  }
  if (claudeTaskGetUnresolved(argsRow, resultRow, context))
    return { kind: 'hidden' }
  const call = claudeToolCallIR(argsRow, resultRow, context)
  // A Claude tool whose own row the transcript HIDES states no sibling for that side,
  // because the sibling the flag promises is never drawn.
  return toolCallRow(call, own.role, {
    request: !!requestSide && !claudeToolRowHidden(spanTool, 'request'),
    result: !!resultSide && !claudeToolRowHidden(spanTool, 'result'),
  })
}
