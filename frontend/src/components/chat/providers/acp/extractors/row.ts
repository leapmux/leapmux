import type { ChatRowIR, ToolCallRow, ToolRowRole } from '../../../ir/row'
import type { ToolCallIR } from '../../../ir/toolCall'
import type { ACPToolCallAdapter } from './toolCall'
import type { RowExtractionInput, ToolSpanSides } from '~/components/chat/rowExtractionTypes'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { ACP_SUPPLEMENT_IDENTITY } from '~/generated/contracts/acp-protocol'
import { pickString } from '~/lib/jsonPick'
import { toolCallRow } from '../../../ir/row'
import { SYNTHETIC_TOOL_LIFECYCLE, toolCall } from '../../../ir/toolCall'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { ACP_SESSION_UPDATE } from '../updateVocabulary'
import { acpPlanTodos } from './plan'
import { acpToolCallIR, acpToolFinished, parsedACPToolCall, resolveACPToolCall } from './toolCall'

/**
 * Read one Agent Client Protocol row into the shared row IR.
 *
 * Every provider of this family shares it and supplies only its own tool adapter.
 * Assistant text and reasoning are absent on purpose: LeapMux assembles a run of text
 * chunks into ONE row carrying the shared assembled-message envelope, and the
 * dispatcher draws that envelope before it reaches any plugin.
 */
export function acpExtractRow(input: RowExtractionInput, callAdapter?: ACPToolCallAdapter): ChatRowIR | null {
  const { category, parsed, sides } = input
  const payload = parsed.parentObject
  if (category.kind === 'tool_use') {
    if (!payload)
      return null
    if (payload.sessionUpdate === ACP_SESSION_UPDATE.PLAN)
      return acpPlanRow(payload)
    return acpToolCallSpanRow(parsedACPToolCall(payload) ?? payload, sides, callAdapter)
  }
  if (category.kind === 'user_content')
    return leapmuxUserRow(payload)
  if (category.kind === 'plan_execution')
    return leapmuxPlanExecutionRow(payload)
  return null
}

/**
 * The checklist row an ACP `plan` update becomes.
 *
 * A plan is a whole-list snapshot rather than a tool call, so it carries no call id
 * and no span. It still draws through the shared tool row, because the checklist body
 * and its header are the same ones every provider's to-do tool draws.
 */
function acpPlanRow(update: Record<string, unknown>): ChatRowIR | null {
  const todos = acpPlanTodos(update.entries)
  if (todos === null)
    return null
  const call = toolCall(
    { id: '', name: ACP_SESSION_UPDATE.PLAN, lifecycle: SYNTHETIC_TOOL_LIFECYCLE },
    { kind: 'todo', label: 'Plan', title: 'Plan', request: { items: todos }, result: { items: todos } },
  )
  return toolCallRow(call, 'result', { request: false, result: false })
}

/** The span id one side's frame carries, or null when the frame states none. */
function acpSideCallId(side: ParsedMessageContent | undefined): string | null {
  return pickString(side?.parentObject, 'toolCallId') || null
}

/** The span row a provider on the new path emits: ONE call, plus the row facts. */
function acpToolCallSpanRow(tool: Record<string, unknown>, sides: ToolSpanSides, callAdapter: ACPToolCallAdapter | undefined): ToolCallRow {
  // Each side is built from its OWN parsed message, with the opener merged in where
  // a later frame omitted a field; which frame ARRIVED decides the row's place.
  // A side from ANOTHER call is no side of this one: `resolveACPToolCall` refuses
  // a mismatched opener, and the row flags must refuse it the same way.
  const own = sides.current ?? undefined
  const callId = pickString(tool, 'toolCallId') || ''
  const openerSide = acpSideCallId(sides.request) === callId ? sides.request : undefined
  const resultSide = acpSideCallId(sides.result) === callId ? sides.result : undefined
  // ONE call from every side: the opener identifies the arguments, the result the
  // payload. Whichever frame this row is, the merged object carries both.
  const openerFrame = openerSide?.parentObject
  const resultFrame = resultSide?.parentObject
  // The role reads the OWN frame, not the merged call: the merge folds the result
  // into a request row, and a request row that then read as a result row would
  // drop its own header and draw a body the frame never carried.
  const finished = acpToolFinished(tool, own?.completion)
  // A row that has not finished renders the span's EARLIER frame, so its call
  // folds the result side in; a finished row IS the result side -- the resolver
  // may hand back a re-parse of that same message, so object identity cannot
  // tell the two apart, and the finished flag must.
  const resolved = !finished && resultFrame
    ? resolveACPToolCall(resultFrame, tool)
    : resolveACPToolCall(tool, openerFrame)
  const call: ToolCallIR = acpToolCallIR(resolved, callAdapter, (resultSide ?? own)?.supplementalContent, own?.completion, { role: sides.role, hasResult: !!resultSide })
  const role: ToolRowRole = finished ? 'result' : pickString(tool, ACP_SUPPLEMENT_IDENTITY.SessionUpdate) === ACP_SESSION_UPDATE.TOOL_CALL ? 'request' : 'update'
  return toolCallRow(call, role, { request: !!openerSide, result: !!resultSide })
}
