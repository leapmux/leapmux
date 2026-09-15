import type { ToolKind } from '../../results/toolKind'
import type { ToolBodySource, ToolMessageSource, ToolPresentation } from '../../results/toolPresentation'
import type { PiTodoSource } from './extractors/todo'
import type { PiToolExecution } from './extractors/toolCommon'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { PI_EVENT, PI_TOOL } from '~/generated/contracts/pi-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { pluralize } from '~/lib/plural'
import { readFileBodyText } from '../../results/readFileResult'
import { toolStatusFor } from '../../results/toolRowStatus'
import { retainedOutcome, retainedRowIsFinal } from '../registry'
import { piAgentRequest, piAgentResult } from './extractors/agent'
import { extractPiCommand, piCommandSource } from './extractors/command'
import { extractPiRead, piEditsFromArgs, piResolveDiffSources, resolvePiResultDiff } from './extractors/fileEdit'
import { piGenericToolSource } from './extractors/generic'
import { piToolResultImages } from './extractors/image'
import { extractPiSearch } from './extractors/search'
import { piTodoSource } from './extractors/todo'
import { piExtractTool, piPairedRequest } from './extractors/toolCommon'
import { piWorkflowRequest, piWorkflowResult } from './extractors/workflow'
import { PI_AGENT_TOOL, PI_POWERSHELL_TOOL, PI_SEARCH_TOOL } from './protocol'

/**
 * The shared tool kind each Pi tool declares.
 *
 * Pi reports a tool by NAME alone, so this table is where the name becomes the closed
 * kind that drives the icon, the label, the title and the input summary. A name that
 * is absent from the table keeps the rich-content row, which is what Pi's extensions
 * return.
 *
 * `plan_mode_complete` is deliberately absent. A plan is not a tool body: it draws
 * through `MarkdownPlanLayout`, which every provider shares, and the renderer routes
 * it before the shared component sees it.
 *
 * A Map rather than an object, here and below: an extension may be called
 * `constructor` or `toString`, and a plain object answers those two names from
 * `Object.prototype` instead of reporting that it holds no entry.
 */
const PI_TOOL_KINDS: ReadonlyMap<string, ToolKind> = new Map<string, ToolKind>([
  [PI_TOOL.Bash, 'execute'],
  [PI_POWERSHELL_TOOL, 'execute'],
  [PI_TOOL.Read, 'read'],
  [PI_TOOL.Write, 'write'],
  [PI_TOOL.Edit, 'edit'],
  [PI_TOOL.Todo, 'todo'],
  [PI_TOOL.Agent, 'agent'],
  [PI_TOOL.SubagentWorkflow, 'agent'],
  [PI_AGENT_TOOL.GetResult, 'agent'],
  [PI_AGENT_TOOL.Steer, 'agent'],
  [PI_SEARCH_TOOL.Grep, 'grep'],
  [PI_SEARCH_TOOL.Find, 'glob'],
  [PI_SEARCH_TOOL.List, 'list'],
])

/** The display name of a tool whose wire name is not the word a reader wants. */
const PI_TOOL_LABELS: ReadonlyMap<string, string> = new Map<string, string>([
  [PI_TOOL.Bash, 'Bash'],
  [PI_POWERSHELL_TOOL, 'PowerShell'],
  [PI_TOOL.Read, 'Read'],
  [PI_TOOL.Write, 'Write'],
  [PI_TOOL.Edit, 'Edit'],
])

/**
 * The tools whose FAILURE the shared row states.
 *
 * Their bodies draw DATA -- a file, a diff, a match list, a plan -- so a failed call
 * has no body of its own, and the row shows the error text under the shared Error
 * header. Every other tool draws its own outcome: a command states its exit code, an
 * agent states its status, and a rich-content row states its error beside its blocks.
 */
const PI_SHARED_ERROR_TOOLS: ReadonlySet<string> = new Set<string>([
  PI_TOOL.PlanComplete,
  PI_TOOL.Read,
  PI_TOOL.Edit,
  PI_TOOL.Write,
  PI_SEARCH_TOOL.Grep,
  PI_SEARCH_TOOL.Find,
  PI_SEARCH_TOOL.List,
])

/**
 * One Pi tool call, resolved from the row and the two events that describe it.
 *
 * Pi's `tool_execution_end` carries no arguments, so a result row reads them from the
 * paired `tool_execution_start`. The to-do source is resolved ONCE here, because both
 * the body and the row's outcome read it: a to-do operation reports its failure in
 * `details.error` while Pi still reports `isError: false`.
 */
export interface PiToolRow {
  payload: Record<string, unknown>
  tool: PiToolExecution
  /** The paired `tool_execution_start`, or undefined when the store resolved none. */
  request: ParsedMessageContent | undefined
  /** The paired `tool_execution_end`, or undefined when the store resolved none. */
  result: ParsedMessageContent | undefined
  /** The resolved checklist of a to-do call, or null for every other tool. */
  todo: PiTodoSource | null
  /** True when this row is the last one of its call. */
  finished: boolean
  /** The call failed, whether Pi flagged it or stated it in its own fields. */
  isError: boolean
}

/** Build one tool row, or null for a row that is not a Pi tool call. */
export function piToolRow(
  parsed: unknown,
  request: ParsedMessageContent | undefined,
  result: ParsedMessageContent | undefined,
  completion?: MessageCompletion,
): PiToolRow | null {
  if (!isObject(parsed))
    return null
  const tool = piExtractTool(parsed)
  if (!tool)
    return null
  // A retained `tool_execution_start` is the call's END: the turn stopped before Pi
  // sent the completion event, so this frame is the last one there is.
  const finished = pickString(parsed, 'type') === PI_EVENT.ToolExecutionEnd || retainedRowIsFinal(completion)
  const todo = tool.toolName === PI_TOOL.Todo ? piTodoSource(parsed, request, result) : null
  return { payload: parsed, tool, request, result, todo, finished, isError: tool.isError || !!todo?.error }
}

/** The arguments of the call, which only the opening event carries. */
function piToolArgs(row: PiToolRow): Record<string, unknown> {
  const paired = pickObject(piPairedRequest(row.payload, row.request)?.parentObject, 'args')
  return Object.keys(row.tool.args).length > 0 ? row.tool.args : paired ?? {}
}

/** The arguments of one call, and what its row says beside them. */
interface PiToolInput {
  input: Record<string, unknown>
  /** The line the row draws under its title, or undefined when it draws none. */
  summary?: string
}

/**
 * The arguments of the call, with an edit's substitutions resolved.
 *
 * Pi states an edit as a LIST of substitutions, and the shared title reads ONE pair.
 * A single substitution therefore becomes that pair, so an edit row states its added
 * and removed line counts. A list of several states its size instead, because no one
 * pair describes it.
 *
 * The list is read ONCE. A second read of the resolved arguments would count the pair
 * this added as a substitution of its own, and report two edits for one.
 */
function piToolInput(row: PiToolRow): PiToolInput {
  const args = piToolArgs(row)
  if (row.tool.toolName !== PI_TOOL.Edit)
    return { input: args }
  const edits = piEditsFromArgs(args)
  if (edits.length === 1)
    return { input: { ...args, oldText: edits[0].oldText, newText: edits[0].newText } }
  return { input: args, summary: edits.length > 1 ? pluralize(edits.length, 'edit') : undefined }
}

/** The text the call returned, or the partial text a stopped turn left behind. */
function piToolText(row: PiToolRow): string {
  return row.tool.result?.text ?? row.tool.partialResult?.text ?? ''
}

/** The rich-content row Pi's extensions and MCP bridges return. */
function piGenericBody(row: PiToolRow): ToolBodySource {
  const source = piGenericToolSource(row.payload, row.request, row.result)
  return source ? { type: 'mcp', source } : { type: 'text' }
}

/** The subagent card of a launch, a workflow run, or one of the two control tools. */
function piAgentRequestSource(row: PiToolRow): ToolPresentation['agentRequest'] {
  return row.tool.toolName === PI_TOOL.SubagentWorkflow
    ? piWorkflowRequest(row.payload, row.request, row.result)
    : piAgentRequest(row.payload, row.request)
}

/**
 * The body of a call that has NOT finished.
 *
 * A checklist and a subagent launch state their content before the call returns. An
 * unrecognized extension states its arguments the same way its result will. Every
 * other kind waits.
 */
function piRequestBody(row: PiToolRow, kind: ToolKind): ToolBodySource {
  if (kind === 'todo')
    return row.todo ? piTodoBody(row.todo) : piGenericBody(row)
  if (kind === '')
    return piGenericBody(row)
  return { type: 'text' }
}

/** The checklist, its empty state and the note about the task the call refers to. */
function piTodoBody(todo: PiTodoSource): ToolBodySource {
  return { type: 'todo', items: todo.list.todos, emptyText: todo.list.emptyText, description: todo.description || undefined }
}

/** The body of a FINISHED call. */
function piResultBody(row: PiToolRow, kind: ToolKind, input: Record<string, unknown>): ToolBodySource {
  const toolName = row.tool.toolName
  if (row.tool.isError && PI_SHARED_ERROR_TOOLS.has(toolName))
    return { type: 'text' }
  if (toolName === PI_TOOL.PlanComplete) {
    const plan = pickString(row.tool.result?.details, 'plan').trim()
    return { type: 'markdown', text: plan || piToolText(row) }
  }
  if (kind === 'agent') {
    return { type: 'agent', source: toolName === PI_TOOL.SubagentWorkflow
      ? piWorkflowResult(row.payload, row.request)
      : piAgentResult(row.payload, row.request) }
  }
  if (kind === 'todo') {
    if (!row.todo)
      return piGenericBody(row)
    return row.todo.error ? { type: 'text' } : piTodoBody(row.todo)
  }
  if (kind === 'execute') {
    const command = extractPiCommand(row.payload)
    return command ? { type: 'command', source: piCommandSource(command) } : { type: 'text' }
  }
  if (kind === 'read') {
    const read = extractPiRead(row.payload, input)
    return read ? { type: 'read', source: read.source } : { type: 'text' }
  }
  if (kind === 'edit' || kind === 'write') {
    const sources = piResolveDiffSources(row.payload, row.request)
    return sources.length > 0 ? { type: 'diff', sources } : { type: 'text' }
  }
  if (kind === 'grep' || kind === 'glob' || kind === 'list') {
    const search = extractPiSearch(row.payload)
    if (!search)
      return piGenericBody(row)
    return kind === 'list'
      ? { type: 'directory', source: { entries: search.filenames.map(path => ({ path })), truncated: search.truncated, notice: search.notice } }
      : { type: 'search', source: search }
  }
  return piGenericBody(row)
}

/**
 * The plain text the row shows when its body draws none.
 *
 * An edit whose diff this build cannot read is the one case that differs from the
 * result text: the row draws the raw diff, so the Copy button hands over the same
 * words.
 */
function piBodyText(row: PiToolRow, kind: ToolKind, body: ToolBodySource, input: Record<string, unknown>): string {
  if (body.type === 'read')
    return readFileBodyText(body.source)
  if (body.type === 'text' && row.finished && !row.tool.isError && (kind === 'edit' || kind === 'write'))
    return resolvePiResultDiff(row.payload, input).rawDiff || piToolText(row)
  if (body.type === 'text' && kind === 'todo' && row.todo?.error)
    return row.todo.error
  return piToolText(row)
}

/** The display model for one Pi tool call. */
export function piToolPresentation(row: PiToolRow): ToolPresentation {
  const toolName = row.tool.toolName
  const kind = PI_TOOL_KINDS.get(toolName) ?? ''
  const label = PI_TOOL_LABELS.get(toolName) ?? ''
  const { input, summary } = piToolInput(row)
  const body = row.finished ? piResultBody(row, kind, input) : piRequestBody(row, kind)
  const agentRequest = kind === 'agent' ? piAgentRequestSource(row) : undefined
  const presentation: ToolPresentation = {
    kind,
    label: agentRequest?.toolName || label || toolName || undefined,
    // A command with no description of its own states the command, which the shared
    // header draws. Falling back to the tool name would put `Bash` above the very
    // command it ran.
    title: kind === 'execute' ? '' : agentRequest?.description || row.todo?.list.title || label || toolName || 'Tool',
    agentRequest,
    input,
    inputText: summary,
    output: piBodyText(row, kind, body, input),
    body,
    commandLanguage: toolName === PI_POWERSHELL_TOOL ? 'powershell' : undefined,
    metadata: row.todo?.metadata.length ? row.todo.metadata : undefined,
    unresolvedTerminals: [],
  }
  return presentation
}

/** One row, as the shared tool component reads it. */
export function piToolMessageSource(row: PiToolRow, completion?: MessageCompletion): ToolMessageSource {
  return {
    id: row.tool.toolCallId,
    role: row.finished ? 'result' : 'request',
    status: toolStatusFor(retainedOutcome(completion), row.isError, row.finished),
    presentation: piToolPresentation(row),
    images: piToolResultImages(row.payload, undefined, row.request),
  }
}
