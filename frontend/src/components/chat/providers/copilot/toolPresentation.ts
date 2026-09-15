import type { AgentRequestSource } from '~/components/chat/results/AgentRequestMessage'
import type { FileEditDiffSource } from '~/components/chat/results/fileEditDiff'
import type { ToolPresentation } from '~/components/chat/results/toolPresentation'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { agentToolPresentation } from '~/components/chat/results/AgentRequestMessage'
import { toolInputPaths } from '~/components/chat/results/toolInputs'
import { toolKind } from '~/components/chat/results/toolKind'
import { COPILOT_EVENT, COPILOT_TOOL } from '~/generated/contracts/copilot-protocol'
import { prettifyArgsJson, prettifyStructuredJson } from '~/lib/jsonFormat'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { messageCompletionFromProto } from '../../assembledMessage'
import { commandIsError } from '../../results/commandResult'
import { parseMcpContentItem } from '../../results/mcpToolCall'
import { todoToolBody } from '../../results/toolPresentation'
import { retainedRowIsFinal } from '../registry'
import { copilotAgentResult } from './agentResult'
import { copilotChecklistItems } from './checklist'
import { copilotPatchRequest } from './patchRequest'
import { copilotEventData, copilotToolKind } from './protocol'
import { copilotReadResult } from './readResult'

/** The trailer Copilot appends to a shell result. It repeats the exit code. */
const SHELL_COMPLETION = /(?:\r?\n)?<shellId: [^\r\n>]+ completed with exit code (-?\d+)>\s*$/

/** The text without that trailer. The row states the exit code in its own place. */
function stripShellTrailer(value: string): string {
  const trailer = value.match(SHELL_COMPLETION)
  return trailer ? value.slice(0, trailer.index) : value
}

/**
 * True when one grep argument asks for context lines around each match.
 *
 * The count decides whether the match total is meaningful: with context lines, one
 * output line is not one match. Only a positive count asks for them, so `0`, `""`,
 * `false` and `null` all leave the total in place.
 */
function requestsContext(value: unknown): boolean {
  if (typeof value === 'number')
    return Number.isFinite(value) && value > 0
  if (typeof value === 'string')
    return Number(value.trim()) > 0
  return false
}

/**
 * One tool call, resolved from the two native events that describe it.
 *
 * `tool.execution_start` carries the name and the arguments; `tool.execution_complete`
 * carries the outcome and nothing that identifies the tool beyond its call ID. A
 * result row therefore reads its name and its arguments from the paired start row --
 * which the shared message resolver supplies -- or, failing that, from the span type
 * the worker recorded.
 */
export interface CopilotToolRow {
  toolCallId: string
  toolName: string
  kind: string
  input: Record<string, unknown>
  /** True once the completion event arrived. */
  finished: boolean
  status: 'in_progress' | 'completed' | 'failed' | 'cancelled'
  /** The completion's `result` object, or its `error` object when the call failed. */
  raw: Record<string, unknown> | null
}

function copilotToolStatus(data: Record<string, unknown>, completion?: MessageCompletion): CopilotToolRow['status'] {
  if (messageCompletionFromProto(completion) === 'interrupted')
    return 'cancelled'
  if (data.success === true)
    return 'completed'
  // The runtime reports an aborted turn's tool as a failure with this code, which is
  // an interruption rather than a fault the user should read as an error.
  return pickString(pickObject(data, 'error'), 'code') === 'interrupted' ? 'cancelled' : 'failed'
}

/**
 * Build one tool row from a persisted event.
 *
 * `request` is the paired `tool.execution_start`, and `spanType` is the worker's own
 * record of the tool name on every span row. Returns null for any row that is not a
 * tool event, so a caller uses it as a guard.
 */
export function copilotToolRow(
  parsed: unknown,
  spanType?: string,
  request?: ParsedMessageContent,
  completion?: MessageCompletion,
): CopilotToolRow | null {
  const started = copilotEventData(parsed, COPILOT_EVENT.ToolStarted)
  const pairedStart = copilotEventData(request?.parentObject, COPILOT_EVENT.ToolStarted)
  if (started) {
    const toolName = pickString(started, 'toolName') || spanType || ''
    // A RETAINED copy of the start frame is the call's end: the turn stopped before
    // the runtime sent a completion, so this frame is all there is. It carries no
    // result, so the row shows its header and nothing under it.
    const retained = retainedRowIsFinal(completion)
    return {
      toolCallId: pickString(started, 'toolCallId'),
      toolName,
      kind: copilotToolKind(toolName),
      input: pickObject(started, 'arguments') ?? {},
      finished: retained,
      status: retained ? 'cancelled' : 'in_progress',
      raw: null,
    }
  }
  const completed = copilotEventData(parsed, COPILOT_EVENT.ToolCompleted)
  if (!completed)
    return null
  const toolCallId = pickString(completed, 'toolCallId')
  // The paired start belongs to this call alone. A start for another call would
  // supply the wrong name and the wrong arguments.
  const paired = pairedStart && pickString(pairedStart, 'toolCallId') === toolCallId ? pairedStart : null
  const toolName = pickString(paired, 'toolName') || spanType || ''
  const status = copilotToolStatus(completed, completion)
  return {
    toolCallId,
    toolName,
    kind: copilotToolKind(toolName),
    input: pickObject(paired, 'arguments') ?? {},
    finished: true,
    status,
    // A FAILURE states its reason in `error`. Reading `result` first there would show
    // whatever partial output the call produced and hide why it stopped. A cancelled
    // call is not a fault: the turn stopped around it, and its `result` holds the
    // output it produced before that, so the result comes first there.
    raw: status === 'failed'
      ? pickObject(completed, 'error') ?? pickObject(completed, 'result')
      : pickObject(completed, 'result') ?? pickObject(completed, 'error'),
  }
}

/** The text the result carries. The detailed form holds more than the model received. */
function copilotOutput(row: CopilotToolRow): string {
  return pickString(row.raw, 'detailedContent', undefined)
    ?? pickString(row.raw, 'content', undefined)
    ?? pickString(row.raw, 'message', undefined)
    ?? ''
}

/**
 * The file change one native edit or create tool states.
 *
 * Copilot's `edit` and `str_replace_editor` carry the replacement directly, and
 * `create` carries the whole new file. Returns null for a call that states no
 * change -- a `str_replace_editor` view, or a shape this build does not read.
 */
function copilotFileEdit(row: CopilotToolRow, input: Record<string, unknown>): FileEditDiffSource | null {
  const filePath = pickString(input, 'path')
  if (!filePath)
    return null
  const command = pickString(input, 'command')
  const fileText = pickString(input, 'file_text', undefined)
  if (row.toolName === COPILOT_TOOL.Create || command === 'create') {
    return fileText === undefined
      ? null
      : { filePath, operation: 'add', oldStr: '', newStr: fileText, structuredPatch: null, showLineNumbers: false }
  }
  const oldStr = pickString(input, 'old_str', undefined)
  const newStr = pickString(input, 'new_str', undefined)
  if (oldStr === undefined && newStr === undefined)
    return null
  return { filePath, operation: 'edit', oldStr: oldStr ?? '', newStr: newStr ?? '', structuredPatch: null, showLineNumbers: false }
}

/**
 * The display model for one Copilot tool call.
 *
 * Every body is a shared component's source, so a Copilot read and a Claude read draw
 * the same way. What is Copilot-specific is only how its native fields fill them.
 */
export function copilotToolPresentation(row: CopilotToolRow): ToolPresentation {
  const output = copilotOutput(row)
  const input = { ...row.input }
  // `view_range` is Copilot's spelling of the offset and the limit the shared read
  // title renders.
  if (row.kind === 'read' && Array.isArray(input.view_range)) {
    const [first, last] = input.view_range
    if (typeof first === 'number' && Number.isSafeInteger(first) && first > 0) {
      input.offset = first
      if (typeof last === 'number' && Number.isSafeInteger(last) && last >= first)
        input.limit = last - first + 1
    }
  }
  // A search states its targets as an array. The shared title and summary read one
  // `path`, so a single target becomes one, and a list stays a list.
  if (row.kind === 'glob' || row.kind === 'grep') {
    const paths = toolInputPaths(input)
    if (paths.length === 1)
      input.path = paths[0]
  }
  const patchText = row.toolName === COPILOT_TOOL.ApplyPatch
    ? pickString(input, 'input') || pickString(input, 'patch')
    : ''
  const requestedChanges = patchText ? copilotPatchRequest(patchText) : null
  const operation = requestedChanges?.length === 1 ? requestedChanges[0] : undefined
  if (operation) {
    input.path = operation.filePath
    if (operation.operation === 'add')
      input.content = operation.newStr
  }
  const presentation: ToolPresentation = {
    kind: operation?.operation === 'add' ? 'write' : operation?.operation === 'delete' ? 'delete' : toolKind(row.kind),
    // A command with no description of its own states the command itself, which the
    // shared header draws. Falling back to the tool name would put the word `bash`
    // above the very command it ran.
    title: row.kind === 'execute'
      ? pickString(input, 'description')
      : pickString(input, 'description') || row.toolName || 'Tool',
    input,
    output,
    body: { type: 'text' },
    unresolvedTerminals: [],
  }
  if (patchText) {
    // Every operation the patch states is a REQUEST, including a move, which carries
    // no diff of its own. Dropping one would hide a file the call is about to touch.
    // A patch this build cannot read stays readable as its own text rather than
    // disappearing behind a bare tool name.
    if (requestedChanges)
      presentation.requestedChanges = requestedChanges
    else
      presentation.inputText = patchText
  }
  else if (row.kind === 'edit' || row.kind === 'write') {
    const edit = copilotFileEdit(row, input)
    if (edit) {
      presentation.kind = edit.operation === 'add' ? 'write' : 'edit'
      // A finished call CHANGED the file; a running one has only asked to. The two
      // read differently, and the shared bodies keep them apart.
      if (row.finished && row.status === 'completed')
        presentation.body = { type: 'diff', sources: [edit] }
      else
        presentation.requestedChanges = [edit]
    }
  }
  if (!row.finished)
    return copilotRequestBody(presentation, row)
  return copilotResultBody(presentation, row, output)
}

/**
 * A request-side body.
 *
 * A to-do list and a subagent launch both state their own content BEFORE the call
 * finishes: the checklist the model wrote, and the instruction the subagent received.
 * Every other kind waits for its result.
 */
function copilotRequestBody(presentation: ToolPresentation, row: CopilotToolRow): ToolPresentation {
  if (row.kind === 'todo')
    return copilotTodoBody(presentation, row)
  if (row.kind === 'agent')
    return agentToolPresentation(presentation, copilotAgentRequest(row))
  return presentation
}

/** Copilot's checklist arrives as a JSON string in one argument. */
function copilotTodoBody(presentation: ToolPresentation, row: CopilotToolRow): ToolPresentation {
  const items = copilotChecklistItems(pickString(row.input, 'todos'))
  return { ...presentation, ...todoToolBody(items) }
}

/**
 * What the subagent was asked to do, for the shared request card.
 *
 * Copilot states the instruction in `description` and the subagent's own label in
 * `name`, and a launch can carry either one. `copilotAgentResult` reads the same
 * pair for the result card, so the two cards state one description.
 */
function copilotAgentRequest(row: CopilotToolRow): AgentRequestSource {
  return {
    toolName: COPILOT_TOOL.Task,
    description: pickString(row.input, 'description') || pickString(row.input, 'name'),
    agentType: pickString(row.input, 'agent_type'),
    prompt: pickString(row.input, 'prompt'),
  }
}

function copilotResultBody(presentation: ToolPresentation, row: CopilotToolRow, output: string): ToolPresentation {
  const failed = row.status === 'failed' || row.status === 'cancelled'
  const mcpStatus = failed ? 'failed' as const : 'completed' as const
  if (row.kind === 'agent')
    return agentToolPresentation(presentation, copilotAgentRequest(row), copilotAgentResult(row, output))
  if (row.kind === 'todo')
    return copilotTodoBody(presentation, row)
  if (row.kind === 'execute')
    return copilotCommandBody(presentation, row, output, mcpStatus)
  const contents = Array.isArray(row.raw?.contents) ? row.raw.contents : null
  const structuredJson = prettifyStructuredJson(row.raw?.structuredContent)
  if (contents || structuredJson) {
    const source = {
      server: '',
      tool: presentation.title,
      argsJson: '',
      content: (contents ?? []).map(parseMcpContentItem),
      structuredJson,
      status: mcpStatus,
    }
    // A tool this build does not recognize has no body of its own, so the rich
    // content IS its result, and that body states the arguments. A recognized one
    // keeps its own body -- a read stays a read -- and the blocks ride beside it,
    // where the header above already states the arguments.
    if (row.kind === 'other')
      return { ...presentation, body: { type: 'mcp', source: { ...source, argsJson: prettifyArgsJson(presentation.input) } } }
    presentation = { ...presentation, additionalContent: source }
  }
  if (failed)
    return presentation
  if (row.kind === 'glob' || row.kind === 'grep')
    return copilotSearchBody(presentation, row, output)
  if (row.kind === 'read' && typeof row.raw?.content === 'string' && pickString(presentation.input, 'path')) {
    const source = copilotReadResult(row.raw, presentation.input)
    return { ...presentation, output: source.fallbackContent, body: { type: 'read', source } }
  }
  return presentation
}

/**
 * A shell result. The runtime states the exit code twice: inside a `shell_exit`
 * content block and, for a synchronous run, as a trailer on the text. The block wins,
 * and the trailer leaves the displayed text.
 */
function copilotCommandBody(
  presentation: ToolPresentation,
  row: CopilotToolRow,
  output: string,
  mcpStatus: 'failed' | 'completed',
): ToolPresentation {
  const contents = Array.isArray(row.raw?.contents) ? row.raw.contents : []
  const exits = contents.filter(isObject).filter(item => item.type === 'shell_exit')
  // The BLOCK and the CODE are two decisions. One `shell_exit` block states the shell,
  // the directory and the output file whether or not it reports a usable code, and the
  // rows below read it for those. Only a safe integer states the code itself.
  const exitBlock = exits.length === 1 ? exits[0] : undefined
  const reportedExit = pickNumber(exitBlock, 'exitCode', undefined)
  const knownExit = reportedExit !== undefined && Number.isSafeInteger(reportedExit) ? reportedExit : undefined
  const trailer = output.match(SHELL_COMPLETION)
  const exitCode = knownExit ?? (trailer ? Number(trailer[1]) : undefined)
  const text = stripShellTrailer(output)
  // Every text the result itself carries is already on the row: the displayed text and
  // each field `copilotOutput` chose between. A content block that repeats one of them
  // would draw the same text a second time.
  const shown = new Set([output, text])
  for (const key of ['content', 'detailedContent', 'message']) {
    const value = pickString(row.raw, key, undefined)
    if (value !== undefined) {
      shown.add(value)
      shown.add(stripShellTrailer(value))
    }
  }
  const extra = contents.filter(item => item !== exitBlock
    && (!isObject(item) || !(item.type === 'text' && shown.has(pickString(item, 'text')))))
  const structuredJson = prettifyStructuredJson(row.raw?.structuredContent)
  return {
    ...presentation,
    output: text,
    body: { type: 'command', source: {
      output: text,
      exitCode,
      isError: commandIsError(row.status === 'failed' ? 'failed' : undefined, exitCode),
      interrupted: row.status === 'cancelled',
    } },
    metadata: [['Shell ID', 'shellId'], ['Directory', 'cwd'], ['Output file', 'outputFilePath']].flatMap(([label, key]) => {
      const value = pickString(exitBlock, key)
      return value ? [{ label, value }] : []
    }),
    additionalContent: extra.length || structuredJson
      ? {
          server: '',
          tool: presentation.title,
          argsJson: '',
          content: extra.map(parseMcpContentItem),
          structuredJson,
          status: mcpStatus,
        }
      : undefined,
  }
}

/** A glob or grep result. Copilot returns its matches as plain lines. */
function copilotSearchBody(presentation: ToolPresentation, row: CopilotToolRow, output: string): ToolPresentation {
  const input = presentation.input
  const glob = row.kind === 'glob'
  const empty = output.trim() === '' || /^No (?:files|matches) (?:found|matched)[.!]?$/i.test(output.trim())
  const lines = empty ? [] : output.trim().split('\n').filter(Boolean)
  const fileList = glob || input.output_mode === 'files_with_matches'
  const countMode = input.output_mode === 'count'
  const counts = countMode ? lines.map(line => line.match(/:(\d+)\s*$/)).filter(match => match !== null) : []
  const hasContext = ['A', 'B', 'C', '-A', '-B', '-C', 'context', 'after_context', 'before_context']
    .some(key => requestsContext(input[key]))
  return {
    ...presentation,
    label: glob ? 'Glob' : 'Grep',
    body: { type: 'search', source: {
      variant: fileList ? 'glob' : countMode ? 'grep' : 'search',
      pattern: pickString(input, 'pattern', undefined),
      filenames: fileList ? lines : [],
      content: fileList || empty ? '' : output,
      numFiles: fileList || countMode ? lines.length : 0,
      numLines: 0,
      matches: !fileList && !countMode && !hasContext ? lines.length : undefined,
      mode: countMode ? 'count' : undefined,
      numMatches: counts.length === lines.length && countMode ? counts.reduce((total, match) => total + Number(match[1]), 0) : undefined,
      truncated: false,
      fallbackContent: empty ? '' : output,
    } },
  }
}
