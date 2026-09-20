import type { ToolCall, ToolCallEnvelope, ToolCallSpec, ToolCallSpecReaderTable, ToolCallSpecVariant, ToolFailureResult } from '../../../model/toolCall'
import type { ToolKind } from '../../../model/toolKind'
import type { ToolRequestByKind } from '../../../model/tools'
import type { GenericToolResult } from '../../../model/tools/generic'
import type { ClaudeRowContext, ClaudeToolRow } from '../extractors/toolCommon'
import { createToolCall } from '../../../model/createToolCall'
import { parseMcpToolName } from '../../../model/mcpToolCall'
import { failedResult, isGenericKind, readToolCallSpec, unparsedResult } from '../../../model/toolCall'
import { retainedOutcome } from '../../registry'
import { claudeAgentSpec } from '../extractors/agent'
import { claudeExecuteSpec } from '../extractors/execute'
import { claudeToolFailureResult } from '../extractors/failure'
import { claudeFileChangeResult } from '../extractors/fileEdit'
import { claudeAgentsSpec } from '../extractors/listAgents'
import { claudeListResourcesSpec } from '../extractors/listResources'
import { claudeMcpSpec } from '../extractors/mcp'
import { claudeMessageSpec } from '../extractors/message'
import { claudeReportSpec, claudeSkillSpec, claudeWaitSpec } from '../extractors/prose'
import { claudeReadSpec } from '../extractors/read'
import { claudeTriggerSpec } from '../extractors/remoteTrigger'
import { claudeGlobSpec, claudeGrepSpec } from '../extractors/search'
import { claudeSwitchModeSpec } from '../extractors/switchMode'
import { claudeTaskSpec } from '../extractors/task'
import { claudeTaskTodoSpec, claudeTodoSpec } from '../extractors/todo'
import { claudeRequestFor } from '../extractors/toolRequests'
import { claudeFetchSpec } from '../extractors/webFetch'
import { claudeWebSearchSpec } from '../extractors/webSearch'
import { claudeToolIcon, claudeToolKind } from '../toolKinds'
import { CLAUDE_TOOL_NAMES } from '../toolNames'
import { claudeQuestionSpec } from './question'

/** Everything one Claude row reads beyond its own bytes. */
export type { ClaudeRowContext } from '../extractors/toolCommon'

/** A tool-call specification with Claude's outcome override. */
export type ClaudeToolCallSpec = ToolCallSpec

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
      frameStatus: 'unstated',
      providerOutcome: row.toolUseResult?.interrupted === true ? 'interrupted' : row.isError === true ? 'failed' : null,
      retainedOutcome: retainedOutcome(context.completion),
      rowFinal: row.role === 'result' || retainedOutcome(context.completion) !== null,
      resultFrameLanded: row.role === 'result',
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
  /** What the row reads beyond its own bytes: the paired payload and the task snapshot. */
  context: ClaudeRowContext
}

/**
 * The kind-specific half of one Claude call.
 *
 * ONE lookup in {@link CLAUDE_TOOL_READERS}: the kind picks the reader, and that reader
 * reads the request arguments and the result data into that kind's
 * typed pair.
 */
export function claudeSpec(args: ClaudeToolRow, result: ClaudeToolRow | undefined, context: ClaudeRowContext): ClaudeToolCallSpec {
  return claudeReaderFor({ args, result, context }, claudeCallKind(args.toolName))
}

/**
 * The kind one Claude call takes.
 *
 * An MCP wire name answers FIRST. Such a name spells its server and its tool inside
 * itself, and `CLAUDE_TOOL_KINDS` holds no entry for one -- so the name table alone
 * answers the unspecified kind, and the call would draw the generic card rather than the MCP
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
function claudeReaderRequest<K extends ToolKind>(kind: K, facts: ClaudeCallFacts): ToolRequestByKind[K] {
  return claudeRequestFor(kind, facts.args.input, { toolName: facts.args.toolName, result: facts.result, context: facts.context })
}

/**
 * One reader for each kind, each checked against its OWN kind's request and result.
 *
 * TOTAL over `ToolKind`, and that is what the table exists for. A `switch` narrows the
 * value it tests and never the kind, so a kind with no case fell to the default branch
 * and built `kind: 'unspecified'` -- the generic request and none of the declared fields.
 * `ToolSearch` reaches `search`, the switch held no case for it, and the only thing
 * that hid the result was a second table two files away: `claudeToolRowHidden` draws
 * neither side of that tool. Nothing checked the two tables against each other. Here a
 * missing kind is a compile error.
 *
 * EVERY entry declares its own return type, and the annotation is load-bearing. The
 * mapped type supplies a contextual signature, which is not an annotated position:
 * TypeScript infers an un-annotated arrow's return type from the literal it returns, so
 * the object loses its freshness before any property is checked and a key no renderer
 * reads rides into the model. `toolTableEntriesAreAnnotated.test.ts` keeps every entry in
 * this form.
 */
export const CLAUDE_TOOL_READERS: ToolCallSpecReaderTable<ClaudeCallFacts> = {
  execute: (facts): ToolCallSpecVariant<'execute'> => claudeExecuteSpec(claudeReaderRequest('execute', facts), facts.result),
  read: (facts): ToolCallSpecVariant<'read'> => claudeReadSpec(claudeReaderRequest('read', facts), facts.result),
  // Two entries for one reading, because each states its OWN kind. `CLAUDE_TOOL_KINDS`
  // maps the four file tools onto these two kinds, and it is now the only place that
  // does: the shared builder read the tool name a second time to choose between them.
  edit: (facts): ToolCallSpecVariant<'edit'> => ({ kind: 'edit', request: claudeReaderRequest('edit', facts), ...claudeFileChangeResult(facts.args, facts.result) }),
  write: (facts): ToolCallSpecVariant<'write'> => ({ kind: 'write', request: claudeReaderRequest('write', facts), ...claudeFileChangeResult(facts.args, facts.result) }),
  grep: (facts): ToolCallSpecVariant<'grep'> => claudeGrepSpec(claudeReaderRequest('grep', facts), facts.result),
  glob: (facts): ToolCallSpecVariant<'glob'> => claudeGlobSpec(claudeReaderRequest('glob', facts), facts.result),
  fetch: (facts): ToolCallSpecVariant<'fetch'> => claudeFetchSpec(claudeReaderRequest('fetch', facts), facts.result),
  web_search: (facts): ToolCallSpecVariant<'web_search'> => claudeWebSearchSpec(claudeReaderRequest('web_search', facts), facts.result),
  agent: (facts): ToolCallSpecVariant<'agent'> => claudeAgentSpec(claudeReaderRequest('agent', facts), facts.args, facts.result),
  todo: (facts): ToolCallSpecVariant<'todo'> => claudeTodoToolSpec(facts),
  question: (facts): ToolCallSpecVariant<'question'> => claudeQuestionSpec(claudeReaderRequest('question', facts), facts.args, facts.result),
  task: (facts): ToolCallSpecVariant<'task'> => claudeTaskSpec(claudeReaderRequest('task', facts), facts.args, facts.result),
  trigger: (facts): ToolCallSpecVariant<'trigger'> => claudeTriggerSpec(claudeReaderRequest('trigger', facts), facts.args, facts.result),
  switch_mode: (facts): ToolCallSpecVariant<'switch_mode'> => claudeSwitchModeSpec(claudeReaderRequest('switch_mode', facts), facts.args, facts.result),
  agents: (facts): ToolCallSpecVariant<'agents'> => claudeAgentsSpec(claudeReaderRequest('agents', facts), facts.args, facts.result),
  message: (facts): ToolCallSpecVariant<'message'> => claudeMessageSpec(claudeReaderRequest('message', facts), facts.args, facts.result),
  skill: (facts): ToolCallSpecVariant<'skill'> => claudeSkillSpec(claudeReaderRequest('skill', facts), facts.result),
  wait: (facts): ToolCallSpecVariant<'wait'> => claudeWaitSpec(claudeReaderRequest('wait', facts), facts.result),
  report: (facts): ToolCallSpecVariant<'report'> => claudeReportSpec(claudeReaderRequest('report', facts), facts.result),
  list: (facts): ToolCallSpecVariant<'list'> => claudeListResourcesSpec(claudeReaderRequest('list', facts), facts.result),
  mcp: (facts): ToolCallSpecVariant<'mcp'> => claudeMcpSpec(claudeReaderRequest('mcp', facts), facts.args, facts.result),
  // The generic card, for a tool no vocabulary lists. Claude's own table answers the
  // `unspecified` for such a name, which means that the provider states no kind.
  unspecified: (facts): ToolCallSpecVariant<'unspecified'> => ({ kind: 'unspecified', request: claudeReaderRequest('unspecified', facts), ...claudeGenericToolResult(facts.result) }),
  // UNREACHABLE, and the one entry here a Claude row could otherwise reach: `other` is
  // the state "the provider called the tool uncategorized", and Claude never says it --
  // `claudeToolKind` answers `unspecified` for every name its table does not hold. The entry
  // states the same card at its OWN kind, so a build that starts producing `other`
  // draws the card rather than an empty row.
  other: (facts): ToolCallSpecVariant<'other'> => ({ kind: 'other', request: claudeReaderRequest('other', facts), ...claudeGenericToolResult(facts.result) }),
  // `ToolSearch` asks which DEFERRED tools exist before the model calls one. The tool
  // registry is a corpus, so `search` is the kind it takes (`model/toolKind.ts`), and the
  // matches are TOOL NAMES. `filenames` and `lines` are the two fields of
  // `SearchResult` that state files, and `searchResultText` relativizes a line as a
  // path -- so a tool name in either one draws as a file the search found. The reader
  // therefore fills neither. `tool_use_result.matches` carries the names, and the
  // result blocks are `tool_reference`, which holds no text -- so the unread reading
  // answers an EMPTY body rather than a wrong one. The query still fills the declared
  // request, which titles the row. Both rows are hidden (`claudeToolRowHidden`), so
  // nothing draws either one.
  search: claudeUnreadKind('search'),
  // The six kinds no Claude tool takes: `CLAUDE_TOOL_KINDS` maps no name to any of
  // them, and no MCP wire name reaches one. With `other` above, seven of the thirty
  // kinds are unreachable and the other twenty-three are what Claude produces.
  chart: claudeUnreadKind('chart'),
  delete: claudeUnreadKind('delete'),
  image: claudeUnreadKind('image'),
  memory: claudeUnreadKind('memory'),
  move: claudeUnreadKind('move'),
  think: claudeUnreadKind('think'),
}

/**
 * One kind's specification, read from the facts. The table covers `ToolKind`.
 *
 * Generic over the kind, which keeps `kind` and the specification one
 * correlated pair. The caller's `ToolKind` satisfies the parameter member by member, so
 * no assertion stands between the table and the result --
 * the assertion ban in `eslint.config.ts` refuses exactly that assertion.
 */
function claudeReaderFor<K extends ToolKind>(facts: ClaudeCallFacts, kind: K): { [P in K]: ToolCallSpecVariant<P> }[K] {
  return readToolCallSpec(CLAUDE_TOOL_READERS, kind, facts)
}

/**
 * The reader of a kind whose answer this build does not read into a shape: the declared
 * request, and the words the call sent.
 *
 * The REQUEST is filled all the same, from the shared table or Claude's own override,
 * so a row that reaches one of these draws the kind's card rather than throwing inside
 * a renderer that reads `request.changes[0]` or `request.path` with no guard.
 */
function claudeUnreadKind<P extends ToolKind>(kind: P): (facts: ClaudeCallFacts) => ToolCallSpecVariant<P> {
  // The inner arrow states its OWN return type, although the signature above already
  // declares it. A contextual signature is not an annotated position, so without this
  // the literal escapes the excess-property check -- the same hole every table entry
  // closes, one level down.
  return (facts): ToolCallSpecVariant<P> => {
    const request = claudeReaderRequest(kind, facts)
    if (!facts.result)
      return { kind, request }
    // A result row EXISTS, so the call answered, and every answered call states a
    // result here. An empty answer takes `unparsedResult('')` rather than none,
    // because a payload with no result reads as a call still in flight.
    return { kind, request, result: claudeToolFailureResult(facts.result) ?? unparsedResult(facts.result.resultContent) }
  }
}

/** The todo family: `TodoWrite` states a list; a `Task*` call states one item. */
function claudeTodoToolSpec(facts: ClaudeCallFacts): ToolCallSpecVariant<'todo'> {
  const request = claudeReaderRequest('todo', facts)
  switch (facts.args.toolName) {
    case CLAUDE_TOOL_NAMES.TASK_CREATE:
      return claudeTaskTodoSpec(request, facts.result, facts.context, 'Task created', facts.args.toolName)
    case CLAUDE_TOOL_NAMES.TASK_UPDATE:
      return claudeTaskTodoSpec(request, facts.result, facts.context, 'Task updated', facts.args.toolName)
    case CLAUDE_TOOL_NAMES.TASK_GET:
      return claudeTaskTodoSpec(request, facts.result, facts.context, 'Task', facts.args.toolName)
    default:
      return claudeTodoSpec(request, facts.args, facts.result)
  }
}

/**
 * The result side the generic card states: the words the tool sent, and its pictures.
 *
 * The pictures ride INSIDE the content, which is what `ToolCallBase.images` states
 * for the generic trio: {@link claudeToolCall} empties the envelope's own list for
 * those kinds, so a screenshot from a tool no vocabulary lists reaches the row and the
 * image tab only from here.
 *
 * This reading does not take `claudeToolFailureResult`, and the pictures are the reason. A
 * `ToolFailureResult` holds TEXT alone, so a failed call that returned pictures keeps them
 * in the content and states its outcome word in `statusOverride` instead. The `mcp`
 * reader stands outside the shared ladder for the same reason.
 */
function claudeGenericToolResult(result: ClaudeToolRow | undefined): { result?: GenericToolResult | ToolFailureResult, statusOverride?: 'failed' } {
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
 * specification, and the envelope's identity and status.
 */
export function claudeToolCall(args: ClaudeToolRow, result: ClaudeToolRow | undefined, context: ClaudeRowContext): ToolCall {
  const spec = claudeSpec(args, result, context)
  const envelope = claudeEnvelope(result ?? args, context)
  // The wire name IS the display name for Claude: every tool is spelled the way
  // a reader wants to see it. An MCP call states its server and tool instead, and
  // no reader specification carries a label or an icon of its own, so each key rides
  // only when this row has one.
  const mcp = parseMcpToolName(args.toolName)
  const label = mcp ? undefined : (args.toolName || undefined)
  const icon = claudeToolIcon(args.toolName)
  return createToolCall(envelope, {
    ...spec,
    ...(label !== undefined ? { label } : {}),
    ...(icon !== undefined ? { icon } : {}),
    // The pictures the result carried, for a kind whose specification names none.
    // The generic trio keeps its pictures in the result's own content blocks.
    images: isGenericKind(spec.kind) ? [] : spec.images ?? result?.images ?? [],
  })
}
