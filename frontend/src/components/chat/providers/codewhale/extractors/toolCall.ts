import type { FileEditDiff } from '../../../model/fileEditDiff'
import type { ProseResult, ToolCall, ToolCallEnvelope, ToolCallSpecReaderTable, ToolCallSpecVariant, ToolFailureResult, UnparsedToolResult } from '../../../model/toolCall'
import type { ToolKind } from '../../../model/toolKind'
import type { ToolRequestByKind } from '../../../model/tools'
import type { FileChangeResult } from '../../../model/tools/fileChange'
import type { GenericToolResult } from '../../../model/tools/generic'
import type { TaskRequest, TaskStatus } from '../../../model/tools/task'
import type { ToolRequestOverrides } from '../../defaultToolRequests'
import type { CodewhaleToolFrame } from './toolCommon'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { CODEWHALE_RESULT_FIELD, CODEWHALE_TOOL, CODEWHALE_WORKFLOW_STATUS } from '~/generated/contracts/codewhale-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { createToolCall } from '../../../model/createToolCall'
import { fileEditDrawsDiff } from '../../../model/fileEditDiff'
import { failedResult, proseResult, readToolCallSpec, unparsedResult } from '../../../model/toolCall'
import { toolRequestFor } from '../../defaultToolRequests'
import { retainedOutcome } from '../../registry'
import { codewhaleQuestionsFromToolInput } from '../askUserQuestion'
import { CODEWHALE_RESULT_METADATA } from '../protocol'
import { codewhaleMcpToolName, codewhaleToolKind } from '../toolKinds'
import { codewhaleAgentRequest, codewhaleAgentRuns } from './agent'
import { codewhaleCommandResult, codewhaleExecuteRequest } from './execute'
import { codewhaleMutationChanges, codewhaleRequestedChanges } from './fileEdit'
import { codewhaleListResult } from './list'
import { codewhaleReadRequest, codewhaleReadResult } from './read'
import { codewhaleCorpusSearchResult, codewhaleFileSearchResult, codewhaleGrepResult } from './search'
import { codewhaleChecklistItems, codewhalePlanNote, codewhaleTodoItems } from './todo'
import { codewhaleFrameFailed, codewhalePairedFrame, codewhaleToolSpanRole } from './toolCommon'

/**
 * The kind each action of the `File` facade performs (`canonical_action.rs`).
 *
 * The facade states its real operation in `action`, one of seven single-purpose tools
 * the runtime keeps as hidden aliases. An action this table does not hold keeps the
 * table's own kind for the facade.
 */
const FILE_ACTION_KINDS: ReadonlyMap<string, ToolKind> = new Map<string, ToolKind>([
  ['read', 'read'],
  ['list', 'list'],
  ['search_name', 'glob'],
  ['search_content', 'grep'],
  ['write', 'write'],
  ['edit', 'edit'],
  ['patch', 'edit'],
])

/** The kind each action of the `Web` facade performs. */
const WEB_ACTION_KINDS: ReadonlyMap<string, ToolKind> = new Map<string, ToolKind>([
  ['search', 'web_search'],
  ['fetch', 'fetch'],
  ['wait', 'wait'],
])

/** The task action each single-purpose background tool performs, by its name. */
const TASK_TOOL_ACTIONS: ReadonlyMap<string, TaskRequest['action']> = new Map<string, TaskRequest['action']>([
  [CODEWHALE_TOOL.TaskShellWait, 'output'],
  [CODEWHALE_TOOL.TerminalWait, 'output'],
  [CODEWHALE_TOOL.TerminalCancel, 'stop'],
])

/** The task action each `action` word of the `tasks` and `workflow` tools states. */
const TASK_ARGUMENT_ACTIONS: ReadonlyMap<string, TaskRequest['action']> = new Map<string, TaskRequest['action']>([
  ['list', 'list'],
  ['read', 'output'],
  ['status', 'output'],
  ['cancel', 'stop'],
  ['stop', 'stop'],
])

/**
 * The run status words of a `workflow` result's metadata, as a task outcome.
 *
 * `degraded` is a run that finished with failed stages, which is a failure to the
 * reader. A word from a later release reads as a run still going, which is the one
 * state that claims no ending.
 */
const WORKFLOW_STATUS_OUTCOMES: ReadonlyMap<string, TaskStatus> = new Map<string, TaskStatus>([
  [CODEWHALE_WORKFLOW_STATUS.Running, 'running'],
  [CODEWHALE_WORKFLOW_STATUS.Completed, 'completed'],
  [CODEWHALE_WORKFLOW_STATUS.Degraded, 'failed'],
  [CODEWHALE_WORKFLOW_STATUS.Failed, 'failed'],
  [CODEWHALE_WORKFLOW_STATUS.Cancelled, 'stopped'],
])

/**
 * Everything one Codewhale tool row states, collected once before any specification
 * decision runs.
 *
 * A call's two halves arrive as separate rows -- the frame that opened it and the frame
 * that ended it -- and each row builds the WHOLE call from both, so the request row and
 * the result row draw one call.
 */
export interface CodewhaleToolFacts {
  /** The resolved tool name. `''` when no source states one. */
  toolName: string
  /** The kind this row takes, after {@link codewhaleReclassify}. */
  kind: ToolKind
  /** The call's arguments, from whichever frame states them. */
  input: Record<string, unknown>
  /** The frame that ended the call, or null while it runs. */
  resultFrame: CodewhaleToolFrame | null
  /** True when the call answered. A retained opening frame is final but states no answer. */
  resultAvailable: boolean
  /** True when the call reported an error. */
  failed: boolean
  /** True when the turn stopped the call before it answered. */
  interrupted: boolean
  /** The words the call answered with. */
  text: string
  /** The result metadata of the frame that ended the call. */
  metadata: Record<string, unknown>
  /** The change a file tool asks for, read from its arguments. */
  requestedChanges: FileEditDiff[]
  /** The change a file tool landed, read from its result. Null when the result states none. */
  landedChanges: FileEditDiff[] | null
}

/** The two sides of one span, as the message store resolved them for a row. */
export interface CodewhaleToolSides {
  request: ParsedMessageContent | undefined
  result: ParsedMessageContent | undefined
}

/** The first of these argument records that states anything. */
function firstStated(...records: Array<Record<string, unknown> | undefined>): Record<string, unknown> {
  return records.find(record => record !== undefined && Object.keys(record).length > 0) ?? {}
}

/**
 * Collect every fact one row states.
 *
 * The OPENING frame supplies the tool name and the arguments, and the FINAL frame the
 * answer. A final item repeats the arguments as a JSON string, so a result row whose
 * request the store did not resolve still states them. A subagent's result block
 * states neither, and the span's `span_type` column names the tool for it.
 */
export function codewhaleToolFacts(own: CodewhaleToolFrame, sides: CodewhaleToolSides, spanType: string | undefined): CodewhaleToolFacts {
  const requestFrame = own.outcome === 'open' ? own : codewhalePairedFrame(own, sides.request)
  const pairedResult = own.outcome === 'open' ? codewhalePairedFrame(own, sides.result) : null
  const resultFrame = own.outcome !== 'open' ? own : pairedResult?.outcome !== 'open' ? pairedResult : null
  const toolName = own.toolName || requestFrame?.toolName || resultFrame?.toolName || spanType || ''
  const input = firstStated(own.input, requestFrame?.input, resultFrame?.input)
  const metadata = resultFrame?.metadata ?? {}
  const facts: CodewhaleToolFacts = {
    toolName,
    kind: codewhaleToolKind(toolName),
    input,
    resultFrame,
    resultAvailable: resultFrame !== null,
    failed: resultFrame !== null && codewhaleFrameFailed(resultFrame),
    interrupted: resultFrame?.outcome === 'interrupted',
    text: resultFrame?.text ?? '',
    metadata,
    requestedChanges: codewhaleRequestedChanges(toolName, input),
    landedChanges: codewhaleMutationChanges(metadata),
  }
  return { ...facts, kind: codewhaleReclassify(facts) }
}

/**
 * The kind a row takes AFTER the name table, as one named step.
 *
 * Each swap states the input that causes it:
 *
 *   - The `File` and `Web` facades state their real operation in `action`.
 *   - A to-do tool whose arguments carry no list states none, so it takes the generic
 *     card rather than an empty checklist.
 *   - A file change that names NO file is not a file change. The model refuses the
 *     pair, so the row keeps the arguments the tool sent on the generic card.
 *   - A row whose tool name is empty takes the generic card too: the label is the only
 *     thing that separates the two, and an empty name supplies none.
 */
export function codewhaleReclassify(facts: CodewhaleToolFacts): ToolKind {
  const action = pickString(facts.input, 'action')
  const kind = facts.toolName === CODEWHALE_TOOL.File
    ? FILE_ACTION_KINDS.get(action) ?? facts.kind
    : facts.toolName === CODEWHALE_TOOL.Web
      ? WEB_ACTION_KINDS.get(action) ?? facts.kind
      : facts.kind
  if (kind === 'todo' && codewhaleTodoItems(facts.toolName, facts.input) === null)
    return 'other'
  if ((kind === 'edit' || kind === 'write') && fileChanges(facts).length === 0)
    return 'other'
  if (kind === 'unspecified')
    return 'other'
  return kind
}

/** The changes a file row states: the arguments' own, or the landed ones for a call whose arguments name no file. */
function fileChanges(facts: CodewhaleToolFacts): FileEditDiff[] {
  return facts.requestedChanges.length > 0 ? facts.requestedChanges : facts.landedChanges ?? []
}

/**
 * One Codewhale tool call, as the kind-discriminated pair.
 *
 * `own` is the row's own frame, and `sides` the span's other rows. Null only for a row
 * that is not a tool frame, which the caller checks first.
 */
export function codewhaleToolCall(own: CodewhaleToolFrame, sides: CodewhaleToolSides, parsed: ParsedMessageContent | undefined, spanType: string | undefined): ToolCall {
  const facts = codewhaleToolFacts(own, sides, spanType)
  const envelope: ToolCallEnvelope = {
    id: own.callId,
    name: facts.toolName,
    lifecycle: {
      // The opening frame states the call is running. A final frame states its outcome
      // through `providerOutcome`, so its own word adds nothing.
      frameStatus: facts.resultAvailable ? 'unstated' : 'in_progress',
      providerOutcome: facts.interrupted ? 'interrupted' : facts.failed ? 'failed' : null,
      retainedOutcome: retainedOutcome(parsed?.completion),
      rowFinal: codewhaleToolSpanRole(own, parsed) === 'result',
      resultFrameLanded: facts.resultAvailable,
    },
  }
  const spec = facts.resultFrame?.deferredLoad ? codewhaleSchemaLoadSpec(facts, facts.kind) : codewhaleSpecFor(facts, facts.kind)
  // The tool's OWN name, which the icon tooltip states. A row that states none falls
  // back to the kind's word, because a name invented here is not one the agent sent.
  const label = spec.label ?? (facts.toolName || undefined)
  return createToolCall(envelope, { ...spec, ...(label !== undefined ? { label } : {}) })
}

/**
 * The call a deferred tool's FIRST call was: its arguments, and the runtime's own
 * words for what it did instead of running.
 *
 * The runtime loaded the tool's schema and ran nothing, and the model calls the tool
 * again. The result row hides (see `codewhaleFrameDrawsNothing`), so the request row
 * states the call whole. No reader of the kind can parse the words, which describe
 * a schema rather than a result, so they stay unparsed.
 */
function codewhaleSchemaLoadSpec<K extends ToolKind>(facts: CodewhaleToolFacts, kind: K): ToolCallSpecVariant<K> {
  return { kind, request: codewhaleRequestFor(kind, facts), result: unparsedResult(facts.text) }
}

/** The action one background-task call performs, from its name and then its `action`. */
function codewhaleTaskAction(facts: CodewhaleToolFacts): TaskRequest['action'] {
  return TASK_TOOL_ACTIONS.get(facts.toolName) ?? TASK_ARGUMENT_ACTIONS.get(pickString(facts.input, 'action')) ?? 'other'
}

/** The query one web search asks, from every spelling the tool accepts. */
function codewhaleWebQuery(args: Record<string, unknown>): string {
  const direct = pickString(args, 'query') || pickString(args, 'q')
  if (direct)
    return direct
  const advanced = Array.isArray(args.search_query) ? args.search_query.find(isObject) : undefined
  return pickString(advanced, 'q') || pickString(advanced, 'query')
}

/**
 * The kinds Codewhale reads DIFFERENTLY from the shared table, and nothing else.
 *
 * PARTIAL by its type, and the key set IS the deviation list. Each entry reads a fact
 * the shared arguments reader cannot: the tool name, a Codewhale spelling, or the
 * landed change. Every other kind takes `DEFAULT_TOOL_REQUESTS`.
 *
 * EVERY entry declares its own return type, because the contextual signature of this
 * mapped type is not an annotated position: an un-annotated arrow loses its literal's
 * freshness and the excess-property check never runs.
 */
export const CODEWHALE_TOOL_REQUEST_OVERRIDES: ToolRequestOverrides<CodewhaleToolFacts> = {
  // The child's name, type and prompt, or the ids a management action acts on.
  agent: (args): ToolRequestByKind['agent'] => codewhaleAgentRequest(args),
  // The command a tool runs, from the tool name where no argument states it.
  execute: (args, facts): ToolRequestByKind['execute'] => codewhaleExecuteRequest(facts.toolName, args),
  // The change the arguments ask for, or the landed one for a call whose arguments
  // this build cannot read.
  edit: (_args, facts): ToolRequestByKind['edit'] => ({ changes: fileChanges(facts) }),
  write: (_args, facts): ToolRequestByKind['write'] => ({ changes: fileChanges(facts) }),
  // The server and the tool, which the runtime folds into the tool NAME.
  mcp: (args, facts): ToolRequestByKind['mcp'] => {
    const identity = codewhaleMcpToolName(facts.toolName)
    return { args, server: identity?.server ?? '', tool: identity?.tool ?? facts.toolName }
  },
  // A notice to the reader states a title and a body. A message to a subagent names
  // the child it goes to.
  message: (args, facts): ToolRequestByKind['message'] => {
    if (facts.toolName === CODEWHALE_TOOL.Notify)
      return { text: [pickString(args, 'title'), pickString(args, 'body')].filter(Boolean).join('\n') }
    const to = pickString(args, 'agent_id') || pickString(args, 'to')
    return { ...(to ? { to } : {}), text: pickString(args, 'message') || pickString(args, 'prompt') || pickString(args, 'text') }
  },
  // The parsed questions, whose shape is Codewhale's own.
  question: (args): ToolRequestByKind['question'] => ({ questions: codewhaleQuestionsFromToolInput(args) }),
  // The file, or the handle and the stored result the two reference tools read.
  read: (args): ToolRequestByKind['read'] => codewhaleReadRequest(args),
  // The action, which the tool name or Codewhale's own `action` word states.
  task: (args, facts): ToolRequestByKind['task'] => {
    const taskId = pickString(args, 'task_id') || pickString(args, 'id') || pickString(args, 'run_id') || pickString(args, 'terminal_id') || pickString(args, 'name')
    return { action: codewhaleTaskAction(facts), ...(taskId ? { taskId } : {}) }
  },
  // The parsed items, and the explanation `update_plan` gives above them.
  todo: (args, facts): ToolRequestByKind['todo'] => {
    const note = codewhalePlanNote(facts.toolName, args)
    return { items: codewhaleTodoItems(facts.toolName, args) ?? [], ...(note ? { note } : {}) }
  },
  // The advanced `search_query` list, which the shared reader does not know.
  web_search: (args): ToolRequestByKind['web_search'] => ({ query: codewhaleWebQuery(args) }),
}

/** One kind's declared request: Codewhale's own reading, or the shared table's. */
function codewhaleRequestFor<K extends ToolKind>(kind: K, facts: CodewhaleToolFacts): ToolRequestByKind[K] {
  return toolRequestFor(kind, facts.input, facts, CODEWHALE_TOOL_REQUEST_OVERRIDES)
}

/** The row's own header words: the description the arguments state, then the tool's name. */
function codewhaleCallTitle(facts: CodewhaleToolFacts): string {
  return pickString(facts.input, 'description') || facts.toolName || 'Tool'
}

/** The failure a call answered with: its own words, or the shared sentence when it gave none. */
function codewhaleFailure(facts: CodewhaleToolFacts): ToolFailureResult {
  return failedResult(facts.text || 'Tool call failed')
}

/** The result side every prose kind shares: none, the failure, or the words. */
function codewhaleProseResult(facts: CodewhaleToolFacts): { result?: ProseResult | ToolFailureResult } {
  if (!facts.resultAvailable)
    return {}
  return facts.failed || facts.interrupted ? { result: codewhaleFailure(facts) } : { result: proseResult(facts.text) }
}

/** The result side of a kind this build reads only as text. */
function codewhaleUnreadResult(facts: CodewhaleToolFacts): { result?: ToolFailureResult | UnparsedToolResult } {
  if (!facts.resultAvailable)
    return {}
  if (facts.failed || facts.interrupted)
    return { result: codewhaleFailure(facts) }
  return facts.text ? { result: unparsedResult(facts.text) } : {}
}

/** The result side the generic card states. */
function codewhaleGenericResult(facts: CodewhaleToolFacts): { result?: GenericToolResult | ToolFailureResult } {
  if (!facts.resultAvailable)
    return {}
  if (facts.failed || facts.interrupted)
    return { result: codewhaleFailure(facts) }
  return { result: { content: facts.text ? [{ type: 'text' as const, text: facts.text }] : [] } }
}

/**
 * The result side `edit` and `write` share: the change the runtime LANDED, then the
 * one the arguments asked for, then the words the tool printed.
 *
 * The landed change comes first because the runtime states it as a real diff of the
 * file, and it lists every file an `apply_patch` touched.
 */
function codewhaleFileChangeResult(facts: CodewhaleToolFacts): { result?: FileChangeResult | ToolFailureResult | UnparsedToolResult } {
  if (!facts.resultAvailable)
    return {}
  if (facts.failed || facts.interrupted)
    return { result: codewhaleFailure(facts) }
  // A landed record whose hunks this build could not place states the files alone.
  // The arguments then state the change better, so they answer when they name one.
  const landed = facts.landedChanges
  if (landed && (landed.some(fileEditDrawsDiff) || facts.requestedChanges.length === 0))
    return { result: { changes: landed } }
  if (facts.requestedChanges.length > 0)
    return { result: { changes: facts.requestedChanges } }
  return { result: unparsedResult(facts.text) }
}

/**
 * The reader of a kind no Codewhale tool reaches: the shared request, and the words
 * the call printed.
 */
function codewhaleArgumentsOnly<P extends ToolKind>(kind: P): (facts: CodewhaleToolFacts) => ToolCallSpecVariant<P> {
  // The inner arrow states its OWN return type, for the reason the table states.
  return (facts): ToolCallSpecVariant<P> => ({ kind, request: codewhaleRequestFor(kind, facts), title: codewhaleCallTitle(facts), ...codewhaleUnreadResult(facts) })
}

/**
 * One reader for each kind, each checked against its OWN kind's request and result.
 *
 * Total over `ToolKind` by the mapped type, so a new kind is a compile error here.
 * Every entry declares its own return type, which `toolTableEntriesAreAnnotated.test.ts`
 * keeps in place: without it the literal escapes the excess-property check.
 */
export const CODEWHALE_TOOL_READERS: ToolCallSpecReaderTable<CodewhaleToolFacts> = {
  read: (facts): ToolCallSpecVariant<'read'> => {
    const request = codewhaleRequestFor('read', facts)
    if (!facts.resultAvailable)
      return { kind: 'read', request }
    if (facts.failed || facts.interrupted)
      return { kind: 'read', request, result: codewhaleFailure(facts) }
    return { kind: 'read', request, result: codewhaleReadResult(facts.text, request) }
  },
  edit: (facts): ToolCallSpecVariant<'edit'> => ({ kind: 'edit', request: codewhaleRequestFor('edit', facts), ...codewhaleFileChangeResult(facts) }),
  write: (facts): ToolCallSpecVariant<'write'> => ({ kind: 'write', request: codewhaleRequestFor('write', facts), ...codewhaleFileChangeResult(facts) }),
  execute: (facts): ToolCallSpecVariant<'execute'> => {
    // NO title: the command states itself in the shared header.
    const request = codewhaleRequestFor('execute', facts)
    if (!facts.resultAvailable)
      return { kind: 'execute', request }
    // A command that RAN states its exit code, and its output is the answer whatever
    // the code says. A call that never ran -- a refusal, a sandbox denial -- states no
    // code, and its words are the reason.
    if (facts.metadata[CODEWHALE_RESULT_METADATA.ExitCode] !== undefined || !(facts.failed || facts.interrupted))
      return { kind: 'execute', request, result: { commands: [codewhaleCommandResult(facts.text, facts.metadata)], unresolvedTerminals: [] } }
    return { kind: 'execute', request, result: codewhaleFailure(facts) }
  },
  glob: (facts): ToolCallSpecVariant<'glob'> => {
    const request = codewhaleRequestFor('glob', facts)
    if (!facts.resultAvailable)
      return { kind: 'glob', request }
    if (facts.failed || facts.interrupted)
      return { kind: 'glob', request, result: codewhaleFailure(facts) }
    return { kind: 'glob', request, result: codewhaleFileSearchResult(facts.text) ?? unparsedResult(facts.text) }
  },
  grep: (facts): ToolCallSpecVariant<'grep'> => {
    const request = codewhaleRequestFor('grep', facts)
    if (!facts.resultAvailable)
      return { kind: 'grep', request }
    if (facts.failed || facts.interrupted)
      return { kind: 'grep', request, result: codewhaleFailure(facts) }
    return { kind: 'grep', request, result: codewhaleGrepResult(facts.text) ?? unparsedResult(facts.text) }
  },
  search: (facts): ToolCallSpecVariant<'search'> => {
    const request = codewhaleRequestFor('search', facts)
    if (!facts.resultAvailable)
      return { kind: 'search', request }
    if (facts.failed || facts.interrupted)
      return { kind: 'search', request, result: codewhaleFailure(facts) }
    return { kind: 'search', request, result: codewhaleCorpusSearchResult(facts.toolName, facts.text) }
  },
  list: (facts): ToolCallSpecVariant<'list'> => {
    const request = codewhaleRequestFor('list', facts)
    if (!facts.resultAvailable)
      return { kind: 'list', request }
    if (facts.failed || facts.interrupted)
      return { kind: 'list', request, result: codewhaleFailure(facts) }
    return { kind: 'list', request, result: codewhaleListResult(facts.text) ?? unparsedResult(facts.text) }
  },
  fetch: (facts): ToolCallSpecVariant<'fetch'> => {
    const request = codewhaleRequestFor('fetch', facts)
    if (!facts.resultAvailable)
      return { kind: 'fetch', request }
    if (facts.failed || facts.interrupted)
      return { kind: 'fetch', request, result: codewhaleFailure(facts) }
    const durationMs = facts.metadata[CODEWHALE_RESULT_METADATA.DurationMs]
    return { kind: 'fetch', request, result: { result: facts.text, ...(typeof durationMs === 'number' ? { durationMs } : {}) } }
  },
  web_search: (facts): ToolCallSpecVariant<'web_search'> => {
    const request = codewhaleRequestFor('web_search', facts)
    if (!facts.resultAvailable)
      return { kind: 'web_search', request }
    if (facts.failed || facts.interrupted)
      return { kind: 'web_search', request, result: codewhaleFailure(facts) }
    return { kind: 'web_search', request, result: { links: [], summary: facts.text } }
  },
  agent: (facts): ToolCallSpecVariant<'agent'> => {
    const request = codewhaleRequestFor('agent', facts)
    const title = request.description || codewhaleCallTitle(facts)
    if (!facts.resultAvailable)
      return { kind: 'agent', request, title }
    if (facts.failed || facts.interrupted)
      return { kind: 'agent', request, title, result: codewhaleFailure(facts) }
    const runs = codewhaleAgentRuns(facts.text, request)
    return { kind: 'agent', request, title, result: runs ? { agents: runs } : unparsedResult(facts.text) }
  },
  task: (facts): ToolCallSpecVariant<'task'> => {
    const request = codewhaleRequestFor('task', facts)
    const title = codewhaleCallTitle(facts)
    if (!facts.resultAvailable)
      return { kind: 'task', request, title }
    if (facts.failed || facts.interrupted)
      return { kind: 'task', request, title, result: codewhaleFailure(facts) }
    // A workflow run states its own status beside the words, which is the answer the
    // reader wants first.
    const status = pickString(facts.metadata, CODEWHALE_RESULT_FIELD.Status)
    const outcome = facts.toolName === CODEWHALE_TOOL.Workflow ? WORKFLOW_STATUS_OUTCOMES.get(status) ?? (status ? 'running' : 'completed') : 'completed'
    return { kind: 'task', request, title, result: { outcome, output: facts.text } }
  },
  todo: (facts): ToolCallSpecVariant<'todo'> => {
    // NEVER empty by accident: `codewhaleReclassify` answers `other` for arguments that
    // carry no list, so a row that reaches this reader states one.
    const request = codewhaleRequestFor('todo', facts)
    if (!facts.resultAvailable)
      return { kind: 'todo', request }
    if (facts.failed || facts.interrupted)
      return { kind: 'todo', request, result: codewhaleFailure(facts) }
    // The runtime's own checklist wins once the call lands: it numbers the items and
    // normalizes their status.
    const items = codewhaleChecklistItems(facts.metadata) ?? request.items
    return { kind: 'todo', request, result: { items, ...(request.note !== undefined ? { note: request.note } : {}) } }
  },
  question: (facts): ToolCallSpecVariant<'question'> => {
    const request = codewhaleRequestFor('question', facts)
    if (!facts.resultAvailable)
      return { kind: 'question', request }
    if (facts.failed || facts.interrupted)
      return { kind: 'question', request, result: codewhaleFailure(facts) }
    // The runtime REDACTS the answers from the call's own result, so the row states
    // none. The saved answer beside it is LeapMux's record of what the reader sent.
    return { kind: 'question', request, result: { answers: [] } }
  },
  mcp: (facts): ToolCallSpecVariant<'mcp'> => ({ kind: 'mcp', request: codewhaleRequestFor('mcp', facts), ...codewhaleGenericResult(facts) }),
  // The prose kinds. Each states its own kind, so a shared generic entry does not put
  // the kind and the request beyond the checker.
  agents: (facts): ToolCallSpecVariant<'agents'> => ({ kind: 'agents', request: codewhaleRequestFor('agents', facts), title: codewhaleCallTitle(facts), ...codewhaleProseResult(facts) }),
  memory: (facts): ToolCallSpecVariant<'memory'> => ({ kind: 'memory', request: codewhaleRequestFor('memory', facts), title: codewhaleCallTitle(facts), ...codewhaleProseResult(facts) }),
  message: (facts): ToolCallSpecVariant<'message'> => ({ kind: 'message', request: codewhaleRequestFor('message', facts), title: codewhaleCallTitle(facts), ...codewhaleProseResult(facts) }),
  report: (facts): ToolCallSpecVariant<'report'> => ({ kind: 'report', request: codewhaleRequestFor('report', facts), title: codewhaleCallTitle(facts), ...codewhaleProseResult(facts) }),
  skill: (facts): ToolCallSpecVariant<'skill'> => ({ kind: 'skill', request: codewhaleRequestFor('skill', facts), title: codewhaleCallTitle(facts), ...codewhaleProseResult(facts) }),
  trigger: (facts): ToolCallSpecVariant<'trigger'> => ({ kind: 'trigger', request: codewhaleRequestFor('trigger', facts), title: codewhaleCallTitle(facts), ...codewhaleProseResult(facts) }),
  wait: (facts): ToolCallSpecVariant<'wait'> => ({ kind: 'wait', request: codewhaleRequestFor('wait', facts), title: codewhaleCallTitle(facts), ...codewhaleProseResult(facts) }),
  // The generic card, for a tool no vocabulary lists.
  other: (facts): ToolCallSpecVariant<'other'> => ({ kind: 'other', request: codewhaleRequestFor('other', facts), ...codewhaleGenericResult(facts) }),
  // UNREACHABLE: `codewhaleReclassify` folds `''` to `other`. The entry exists because
  // the table is total, and it states the same card at its own kind.
  unspecified: (facts): ToolCallSpecVariant<'unspecified'> => ({ kind: 'unspecified', request: codewhaleRequestFor('unspecified', facts), ...codewhaleGenericResult(facts) }),
  // The kinds no Codewhale tool takes: the name table maps no tool to them, and no
  // action reaches one.
  chart: codewhaleArgumentsOnly('chart'),
  delete: codewhaleArgumentsOnly('delete'),
  image: codewhaleArgumentsOnly('image'),
  move: codewhaleArgumentsOnly('move'),
  switch_mode: codewhaleArgumentsOnly('switch_mode'),
  think: codewhaleArgumentsOnly('think'),
}

/** The specification of one kind, read from the facts. The table covers `ToolKind`. */
function codewhaleSpecFor<K extends ToolKind>(facts: CodewhaleToolFacts, kind: K): ToolCallSpecVariant<K> {
  return readToolCallSpec(CODEWHALE_TOOL_READERS, kind, facts)
}
