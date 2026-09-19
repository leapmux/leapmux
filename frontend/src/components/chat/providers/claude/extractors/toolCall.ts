import type { FailedResult, ToolCallEnvelope, ToolCallIR, ToolCallPayloadForKind, ToolCallPayloadIR } from '../../../ir/toolCall'
import type { ToolKind } from '../../../ir/toolKind'
import type { ToolRequests } from '../../../ir/tools'
import type { GenericResult } from '../../../ir/tools/generic'
import type { ClaudeRowContext, ClaudeToolRow } from '../extractors/toolCommon'
import { parseMcpToolName } from '../../../ir/mcpToolCall'
import { failedResult, isGenericKind, toolCall, unparsedResult } from '../../../ir/toolCall'
import { retainedOutcome } from '../../registry'
import { claudeAgentPayload } from '../extractors/agent'
import { claudeExecutePayload } from '../extractors/execute'
import { claudeFailedResult } from '../extractors/failure'
import { claudeFileChangeResult } from '../extractors/fileEdit'
import { claudeAgentsPayload } from '../extractors/listAgents'
import { claudeListResourcesPayload } from '../extractors/listResources'
import { claudeMcpPayload } from '../extractors/mcp'
import { claudeMessagePayload } from '../extractors/message'
import { claudeReportPayload, claudeSkillPayload, claudeWaitPayload } from '../extractors/prose'
import { claudeReadPayload } from '../extractors/read'
import { claudeTriggerPayload } from '../extractors/remoteTrigger'
import { claudeGlobPayload, claudeGrepPayload } from '../extractors/search'
import { claudeSwitchModePayload } from '../extractors/switchMode'
import { claudeTaskPayload } from '../extractors/task'
import { claudeTaskTodoPayload, claudeTodoPayload } from '../extractors/todo'
import { claudeRequestFor } from '../extractors/toolRequests'
import { claudeFetchPayload } from '../extractors/webFetch'
import { claudeWebSearchPayload } from '../extractors/webSearch'
import { claudeToolIcon, claudeToolKind } from '../toolKinds'
import { CLAUDE_TOOL_NAMES } from '../toolNames'
import { claudeQuestionPayload } from './question'

/** Everything one Claude row reads beyond its own bytes. */
export type { ClaudeRowContext } from '../extractors/toolCommon'

/** A kind payload, with the one Claude-specific extra: an outcome-word override. */
export type ClaudePayload = ToolCallPayloadIR

/**
 * The provider's envelope: the call's own identity, and its lifecycle as RAW FACTS.
 *
 * Claude sends no status word of its own -- `frameStatus` is empty -- so the facts
 * are the ones its blocks state: an `interrupted` Bash payload or an `is_error`
 * result block is the provider's own conclusion, the completion column carries
 * what LeapMux retained, and a landed result row is the one reading of "finished"
 * the protocol has. The shared derivation owns the precedence between them.
 */
export function claudeEnvelope(row: ClaudeToolRow, context: ClaudeRowContext): ToolCallEnvelope {
  return {
    id: row.id,
    name: row.toolName,
    lifecycle: {
      frameStatus: '',
      providerOutcome: row.toolUseResult?.interrupted === true ? 'interrupted' : row.isError === true ? 'failed' : null,
      retainedOutcome: retainedOutcome(context.completion),
      resultLanded: row.role === 'result',
    },
  }
}

/**
 * Everything one Claude READER reads: both sides of the span, and the row's
 * surroundings.
 *
 * The superset of `ClaudeToolFacts`, which the REQUEST table takes.
 * {@link claudeReaderRequest} builds that one from this, so the tool name is spelled
 * ONCE -- on the request row that carries it.
 *
 * There is no `finished` field here, and Claude needs none. It states a result in a
 * separate `user` message, so an absent {@link ClaudeCallFacts.result} means that no
 * result exists -- unlike the providers whose last frame carries partial output, where
 * the two states read the same and a reader must be told which one it has.
 */
export interface ClaudeCallFacts {
  /** The REQUEST row: the arguments the call sent, and the tool that sent them. */
  args: ClaudeToolRow
  /** The paired RESULT row, or undefined while the call runs. */
  result: ClaudeToolRow | undefined
  /** What the row reads beyond its own bytes: the paired payload and the to-do store. */
  context: ClaudeRowContext
}

/**
 * The kind-specific half of one Claude call.
 *
 * ONE lookup in {@link CLAUDE_TOOL_READERS}: the kind picks the reader, and that reader
 * reads the request side's arguments and the result side's payload into that kind's
 * typed pair.
 */
export function claudePayload(args: ClaudeToolRow, result: ClaudeToolRow | undefined, context: ClaudeRowContext): ClaudePayload {
  return claudeReaderFor({ args, result, context }, claudeCallKind(args.toolName))
}

/**
 * The kind one Claude call takes.
 *
 * An MCP wire name answers FIRST. Such a name spells its server and its tool inside
 * itself, and `CLAUDE_TOOL_KINDS` holds no entry for one -- so the name table alone
 * answers the empty kind, and the call would draw the generic card rather than the MCP
 * one.
 */
function claudeCallKind(toolName: string): ToolKind {
  return parseMcpToolName(toolName) ? 'mcp' : claudeToolKind(toolName)
}

/**
 * One kind's declared request: Claude's own reading, or the shared table's.
 *
 * The REQUEST half of every kind comes from here, so Claude's whole deviation list is
 * the keys of `CLAUDE_TOOL_REQUEST_OVERRIDES`. Each reader below asks for its OWN
 * kind's request, so the kind and the request it answers stay one checked pair.
 *
 * A function DECLARATION rather than a generic arrow. The JSX ban in `eslint.config.ts`
 * reads a `.tsx` module's markup, and a `.ts` file cannot hold JSX at all; the
 * declaration form predates that and reads just as well. {@link claudeReaderFor} takes
 * the same form.
 */
function claudeReaderRequest<K extends ToolKind>(kind: K, facts: ClaudeCallFacts): ToolRequests[K] {
  return claudeRequestFor(kind, facts.args.input, { toolName: facts.args.toolName, result: facts.result, context: facts.context })
}

/**
 * One reader for each kind, each checked against its OWN kind's request and result.
 *
 * TOTAL over `ToolKind`, and that is what the table exists for. A `switch` narrows the
 * value it tests and never the kind, so a kind with no case fell to the default branch
 * and built `kind: ''` -- the empty request, and none of the fields the kind declares.
 * `ToolSearch` reaches `search`, the switch held no case for it, and the only thing
 * that hid the result was a second table two files away: `claudeToolRowHidden` draws
 * neither side of that tool. Nothing checked the two tables against each other. Here a
 * missing kind is a compile error.
 *
 * EVERY entry declares its own return type, and the annotation is load-bearing. The
 * mapped type supplies a contextual signature, which is not an annotated position:
 * TypeScript infers an un-annotated arrow's return type from the literal it returns, so
 * the object loses its freshness before any property is checked and a key no renderer
 * reads rides into the IR. `toolTableEntriesAreAnnotated.test.ts` keeps every entry in
 * this form.
 */
export const CLAUDE_TOOL_READERS: { [K in ToolKind]: (facts: ClaudeCallFacts) => ToolCallPayloadForKind<K> } = {
  'execute': (facts): ToolCallPayloadForKind<'execute'> => claudeExecutePayload(claudeReaderRequest('execute', facts), facts.result),
  'read': (facts): ToolCallPayloadForKind<'read'> => claudeReadPayload(claudeReaderRequest('read', facts), facts.result),
  // Two entries for one reading, because each states its OWN kind. `CLAUDE_TOOL_KINDS`
  // maps the four file tools onto these two kinds, and it is now the only place that
  // does: the shared builder read the tool name a second time to choose between them.
  'edit': (facts): ToolCallPayloadForKind<'edit'> => ({ kind: 'edit', request: claudeReaderRequest('edit', facts), ...claudeFileChangeResult(facts.args, facts.result) }),
  'write': (facts): ToolCallPayloadForKind<'write'> => ({ kind: 'write', request: claudeReaderRequest('write', facts), ...claudeFileChangeResult(facts.args, facts.result) }),
  'grep': (facts): ToolCallPayloadForKind<'grep'> => claudeGrepPayload(claudeReaderRequest('grep', facts), facts.result),
  'glob': (facts): ToolCallPayloadForKind<'glob'> => claudeGlobPayload(claudeReaderRequest('glob', facts), facts.result),
  'fetch': (facts): ToolCallPayloadForKind<'fetch'> => claudeFetchPayload(claudeReaderRequest('fetch', facts), facts.result),
  'web_search': (facts): ToolCallPayloadForKind<'web_search'> => claudeWebSearchPayload(claudeReaderRequest('web_search', facts), facts.result),
  'agent': (facts): ToolCallPayloadForKind<'agent'> => claudeAgentPayload(claudeReaderRequest('agent', facts), facts.args, facts.result),
  'todo': (facts): ToolCallPayloadForKind<'todo'> => claudeTodoToolPayload(facts),
  'question': (facts): ToolCallPayloadForKind<'question'> => claudeQuestionPayload(claudeReaderRequest('question', facts), facts.args, facts.result),
  'task': (facts): ToolCallPayloadForKind<'task'> => claudeTaskPayload(claudeReaderRequest('task', facts), facts.args, facts.result),
  'trigger': (facts): ToolCallPayloadForKind<'trigger'> => claudeTriggerPayload(claudeReaderRequest('trigger', facts), facts.args, facts.result),
  'switch_mode': (facts): ToolCallPayloadForKind<'switch_mode'> => claudeSwitchModePayload(claudeReaderRequest('switch_mode', facts), facts.args, facts.result),
  'agents': (facts): ToolCallPayloadForKind<'agents'> => claudeAgentsPayload(claudeReaderRequest('agents', facts), facts.args, facts.result),
  'message': (facts): ToolCallPayloadForKind<'message'> => claudeMessagePayload(claudeReaderRequest('message', facts), facts.args, facts.result),
  'skill': (facts): ToolCallPayloadForKind<'skill'> => claudeSkillPayload(claudeReaderRequest('skill', facts), facts.result),
  'wait': (facts): ToolCallPayloadForKind<'wait'> => claudeWaitPayload(claudeReaderRequest('wait', facts), facts.result),
  'report': (facts): ToolCallPayloadForKind<'report'> => claudeReportPayload(claudeReaderRequest('report', facts), facts.result),
  'list': (facts): ToolCallPayloadForKind<'list'> => claudeListResourcesPayload(claudeReaderRequest('list', facts), facts.result),
  'mcp': (facts): ToolCallPayloadForKind<'mcp'> => claudeMcpPayload(claudeReaderRequest('mcp', facts), facts.args, facts.result),
  // The generic card, for a tool no vocabulary lists. Claude's own table answers the
  // EMPTY kind for such a name, which is the state "the provider states no kind".
  '': (facts): ToolCallPayloadForKind<''> => ({ kind: '', request: claudeReaderRequest('', facts), ...claudeGenericResult(facts.result) }),
  // UNREACHABLE, and the one entry here a Claude row could otherwise reach: `other` is
  // the state "the provider called the tool uncategorized", and Claude never says it --
  // `claudeToolKind` answers `''` for every name its table does not hold. The entry
  // states the same card at its OWN kind, so a build that starts producing `other`
  // draws the card rather than an empty row.
  'other': (facts): ToolCallPayloadForKind<'other'> => ({ kind: 'other', request: claudeReaderRequest('other', facts), ...claudeGenericResult(facts.result) }),
  // `ToolSearch` asks which DEFERRED tools exist before the model calls one. The tool
  // registry is a corpus, so `search` is the kind it takes (`ir/toolKind.ts`), and the
  // matches are TOOL NAMES. `filenames` and `lines` are the two fields of
  // `SearchResult` that state files, and `searchResultText` relativizes a line as a
  // path -- so a tool name in either one draws as a file the search found. The reader
  // therefore fills neither. `tool_use_result.matches` carries the names, and the
  // result blocks are `tool_reference`, which holds no text -- so the unread reading
  // answers an EMPTY body rather than a wrong one. The query still fills the declared
  // request, which titles the row. Both rows are hidden (`claudeToolRowHidden`), so
  // nothing draws either one.
  'search': claudeUnreadKind('search'),
  // The six kinds no Claude tool takes: `CLAUDE_TOOL_KINDS` maps no name to any of
  // them, and no MCP wire name reaches one. With `other` above, seven of the thirty
  // kinds are unreachable and the other twenty-three are what Claude produces.
  'chart': claudeUnreadKind('chart'),
  'delete': claudeUnreadKind('delete'),
  'image': claudeUnreadKind('image'),
  'memory': claudeUnreadKind('memory'),
  'move': claudeUnreadKind('move'),
  'think': claudeUnreadKind('think'),
}

/**
 * One kind's payload, read from the facts. Total over `ToolKind` by the table.
 *
 * GENERIC over the kind, which is what keeps `kind` and the payload it answers one
 * correlated pair. The caller's `ToolKind` satisfies the parameter member by member, so
 * no assertion stands between the table and the result --
 * the assertion ban in `eslint.config.ts` refuses exactly that assertion.
 */
function claudeReaderFor<K extends ToolKind>(facts: ClaudeCallFacts, kind: K): { [P in K]: ToolCallPayloadForKind<P> }[K] {
  return CLAUDE_TOOL_READERS[kind](facts)
}

/**
 * The reader of a kind whose answer this build does not read into a shape: the declared
 * request, and the words the call sent.
 *
 * The REQUEST is filled all the same, from the shared table or Claude's own override,
 * so a row that reaches one of these draws the kind's card rather than throwing inside
 * a renderer that reads `request.changes[0]` or `request.path` with no guard.
 */
function claudeUnreadKind<P extends ToolKind>(kind: P): (facts: ClaudeCallFacts) => ToolCallPayloadForKind<P> {
  // The inner arrow states its OWN return type, although the signature above already
  // declares it. A contextual signature is not an annotated position, so without this
  // the literal escapes the excess-property check -- the same hole every table entry
  // closes, one level down.
  return (facts): ToolCallPayloadForKind<P> => {
    const request = claudeReaderRequest(kind, facts)
    if (!facts.result)
      return { kind, request }
    // A result row EXISTS, so the call answered, and every answered call states a
    // result here. An empty answer takes `unparsedResult('')` rather than none,
    // because a payload with no result reads as a call still in flight.
    return { kind, request, result: claudeFailedResult(facts.result) ?? unparsedResult(facts.result.resultContent) }
  }
}

/** The todo family: `TodoWrite` states a list; a `Task*` call states one item. */
function claudeTodoToolPayload(facts: ClaudeCallFacts): ToolCallPayloadForKind<'todo'> {
  const request = claudeReaderRequest('todo', facts)
  switch (facts.args.toolName) {
    case CLAUDE_TOOL_NAMES.TASK_CREATE:
      return claudeTaskTodoPayload(request, facts.result, facts.context, 'Task created')
    case CLAUDE_TOOL_NAMES.TASK_UPDATE:
      return claudeTaskTodoPayload(request, facts.result, facts.context, 'Task updated')
    case CLAUDE_TOOL_NAMES.TASK_GET:
      return claudeTaskTodoPayload(request, facts.result, facts.context, 'Task')
    default:
      return claudeTodoPayload(request, facts.args, facts.result)
  }
}

/**
 * The result side the generic card states: the words the tool sent, and its pictures.
 *
 * The pictures ride INSIDE the content, which is what `ToolCallCommon.images` states
 * for the generic trio: {@link claudeToolCallIR} empties the envelope's own list for
 * those kinds, so a screenshot from a tool no vocabulary lists reaches the row and the
 * image tab only from here.
 *
 * This reading does not take `claudeFailedResult`, and the pictures are the reason. A
 * `FailedResult` holds TEXT alone, so a failed call that returned pictures keeps them
 * in the content and states its outcome word in `statusOverride` instead. The `mcp`
 * reader stands outside the shared ladder for the same reason.
 */
function claudeGenericResult(result: ClaudeToolRow | undefined): { result?: GenericResult | FailedResult, statusOverride?: 'failed' } {
  if (!result)
    return {}
  const images = result.images.map(source => ({ type: 'image' as const, source }))
  if (result.isError === true) {
    return images.length > 0
      ? { result: { content: [{ type: 'text', text: result.resultContent }, ...images] }, statusOverride: 'failed' }
      : { result: failedResult(result.resultContent) }
  }
  const text = result.resultContent ? [{ type: 'text' as const, text: result.resultContent }] : []
  return { result: { content: [...text, ...images] } }
}

/**
 * One Claude call joined from its sides: the request's arguments, the result's
 * payload, and the envelope's identity and status.
 */
export function claudeToolCallIR(args: ClaudeToolRow, result: ClaudeToolRow | undefined, context: ClaudeRowContext): ToolCallIR {
  const payload = claudePayload(args, result, context)
  const envelope = claudeEnvelope(result ?? args, context)
  // The wire name IS the display name for Claude: every tool is spelled the way
  // a reader wants to see it. An MCP call states its server and tool instead, and
  // no reader payload carries a label or an icon of its own, so each key rides
  // only when this row has one.
  const mcp = parseMcpToolName(args.toolName)
  const label = mcp ? undefined : (args.toolName || undefined)
  const icon = claudeToolIcon(args.toolName)
  return toolCall(envelope, {
    ...payload,
    ...(label !== undefined ? { label } : {}),
    ...(icon !== undefined ? { icon } : {}),
    // The pictures the result carried, for a kind whose payload names none.
    // The generic trio keeps its pictures in the result's own content blocks.
    images: isGenericKind(payload.kind) ? [] : payload.images ?? result?.images ?? [],
  })
}
