import type { ChatRow, ToolCallRow, ToolSpanRowRole } from '../../../model/row'
import type { ToolCall } from '../../../model/toolCall'
import type { ACPToolCallAdapter } from './toolCall'
import type { RowExtractionInput, ToolSpanContext } from '~/components/chat/rowExtractionTypes'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { ACP_SUPPLEMENT_IDENTITY } from '~/generated/contracts/acp-protocol'
import { pickString } from '~/lib/jsonPick'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { createToolCall } from '../../../model/createToolCall'
import { toolCallRow } from '../../../model/row'
import { SYNTHETIC_TOOL_LIFECYCLE } from '../../../model/toolCallLifecycle'
import { ACP_SESSION_UPDATE } from '../updateVocabulary'
import { acpPlanTodos } from './plan'
import { acpToolCall, acpToolFinished, parsedACPToolCall, resolveACPToolCall } from './toolCall'

/**
 * Read one Agent Client Protocol row into the shared row model.
 *
 * Every provider of this family shares it and supplies only its own tool adapter.
 * Assistant text and reasoning are absent on purpose: LeapMux assembles a run of text
 * chunks into ONE row carrying the shared assembled-message envelope, and the
 * dispatcher draws that envelope before it reaches any plugin.
 */
export function acpExtractRow(input: RowExtractionInput, callAdapter?: ACPToolCallAdapter): ChatRow | null {
  const { category, resolved: parsed, span } = input
  const payload = parsed.parentObject
  if (category.kind === 'tool_use') {
    if (!payload)
      return null
    if (payload.sessionUpdate === ACP_SESSION_UPDATE.PLAN)
      return acpPlanRow(payload)
    if (span.role === 'none')
      return null
    return acpToolCallSpanRow(parsedACPToolCall(payload) ?? payload, parsed, span, callAdapter)
  }
  if (category.kind === 'user_content')
    return leapmuxUserRow(payload)
  if (category.kind === 'plan_execution')
    return leapmuxPlanExecutionRow(payload)
  return null
}

/** Bind one provider's tool adapter to the shared ACP row reader. */
export function createACPRowExtractor(callAdapter?: ACPToolCallAdapter): (input: RowExtractionInput) => ChatRow | null {
  return input => acpExtractRow(input, callAdapter)
}

/**
 * The checklist row an ACP `plan` update becomes.
 *
 * A plan is a whole-list snapshot rather than a tool call, so it carries no call id
 * and no span. It still draws through the shared tool row, because the checklist body
 * and its header are the same ones every provider's to-do tool draws.
 */
function acpPlanRow(update: Record<string, unknown>): ChatRow | null {
  const todos = acpPlanTodos(update.entries)
  if (todos === null)
    return null
  const call = createToolCall(
    { id: '', name: ACP_SESSION_UPDATE.PLAN, lifecycle: SYNTHETIC_TOOL_LIFECYCLE },
    {
      kind: 'todo',
      label: 'Plan',
      title: 'Plan',
      request: { items: todos },
      result: { items: todos },
    },
  )
  return toolCallRow(call, 'result', { request: false })
}

/**
 * The span row a provider on the new path emits: ONE call, plus the row facts.
 *
 * A row reads its OWN frame. A FINISHED row also reads the span's request, because
 * the update that closes a call often omits the fields that the request stated: the
 * kind, the title, the arguments and the locations. An unfinished row reads its own
 * frame alone. A request row is that frame, and an update row (a Goose subagent
 * request) states its whole call in its own frame.
 */
function acpToolCallSpanRow(tool: Record<string, unknown>, own: ParsedMessageContent, span: ToolSpanContext, callAdapter: ACPToolCallAdapter | undefined): ToolCallRow {
  // The role reads the OWN frame, not the merged call: the request merges the kind and
  // the arguments into a result row, and a frame's own status decides its place.
  const finished = acpToolFinished(tool, own.completion)
  // `resolveACPToolCall` refuses a request of ANOTHER call, which is no side of this one.
  const requestFrame = finished
    ? span.request?.parentObject
    : undefined
  const call: ToolCall = acpToolCall(resolveACPToolCall(tool, requestFrame), callAdapter, own.supplementalContent, own.completion, {
    ...(own.agentSessionId === undefined
      ? {}
      : { agentSessionId: own.agentSessionId }),
  })
  const role: ToolSpanRowRole = finished
    ? 'result'
    : pickString(tool, ACP_SUPPLEMENT_IDENTITY.SessionUpdate) === ACP_SESSION_UPDATE.TOOL_CALL
      ? 'request'
      : 'update'
  return toolCallRow(call, role, span.visibleRows)
}
