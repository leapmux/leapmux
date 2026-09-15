import type { ToolKind } from '../../results/toolKind'
import type { ToolBodySource, ToolMessageSource, ToolPresentation } from '../../results/toolPresentation'
import type { ZCodeRow, ZCodeToolUpdate } from './extractors/toolCommon'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { ZCODE_TOOL, ZCODE_TOOL_KIND } from '~/generated/contracts/zcode-protocol'
import { prettifyArgsJson } from '~/lib/jsonFormat'
import { pickFirstString, pickString } from '~/lib/jsonPick'
import { readFileBodyText } from '../../results/readFileResult'
import { TOOL_FILE_PATH_KEYS } from '../../results/toolInputs'
import { todoToolBody } from '../../results/toolPresentation'
import { retainedOutcome } from '../registry'
import { zcodeAgentResult } from './extractors/agent'
import { extractZCodeBash, zcodeBashToCommandSource } from './extractors/bash'
import { zcodeResultDisplay } from './extractors/display'
import { extractZCodeFileDiff, extractZCodeRead, zcodeFilePath } from './extractors/fileEdit'
import { zcodeToolResultImages } from './extractors/image'
import { extractZCodeSearch } from './extractors/search'
import { zcodeErrorText, zcodeExtractTool, zcodeTodoItemsFromInput, zcodeToolInput, zcodeToolSpanRole } from './extractors/toolCommon'
import { ZCODE_WEB_FETCH } from './protocol'

/**
 * The shared tool kind each ZCode tool declares.
 *
 * ZCode reports a tool by NAME alone, so this table is where the name becomes the
 * closed kind that drives the icon, the label, the title and the input summary. A
 * name that is absent from the table keeps the generic row, which states the name
 * and repeats the arguments -- the same answer the Agent Client Protocol gives for
 * its own `other` kind.
 *
 * `ExitPlanMode` is deliberately absent. A plan is not a tool body: it draws through
 * `MarkdownPlanLayout`, which every provider shares, and the renderer routes it
 * before it builds a presentation.
 *
 * A Map rather than an object: a Model Context Protocol tool may be called
 * `constructor` or `toString`, and a plain object answers those two names from
 * `Object.prototype` instead of reporting that it holds no entry.
 */
const ZCODE_TOOL_KINDS: ReadonlyMap<string, ToolKind> = new Map<string, ToolKind>([
  [ZCODE_TOOL.Bash, 'execute'],
  [ZCODE_TOOL.Read, 'read'],
  [ZCODE_TOOL.Write, 'write'],
  [ZCODE_TOOL.Edit, 'edit'],
  [ZCODE_TOOL.Glob, 'glob'],
  [ZCODE_TOOL.Grep, 'grep'],
  [ZCODE_TOOL.Agent, 'agent'],
  [ZCODE_TOOL.TodoWrite, 'todo'],
  [ZCODE_WEB_FETCH, 'fetch'],
])

/** The tools whose input states a file the row titles itself with. */
const ZCODE_FILE_KINDS: ReadonlySet<ToolKind> = new Set<ToolKind>(['read', 'write', 'edit'])

/** The kind of one ZCode tool. An empty name states no kind; an unknown one is uncategorized. */
export function zcodeToolKind(toolName: string): ToolKind {
  return ZCODE_TOOL_KINDS.get(toolName) ?? (toolName ? 'other' : '')
}

/** True when this row is the last one of its tool call. */
function zcodeToolFinished(update: ZCodeToolUpdate, parsed: ParsedMessageContent | undefined): boolean {
  return zcodeToolSpanRole(update.kind, parsed) === 'result'
}

/**
 * How the call ended, in the shared vocabulary the row's header reads.
 *
 * LeapMux's own completion wins over the frame, for the reason `retainedOutcome`
 * gives: a retained frame still reads as a call in progress.
 */
function zcodeToolStatus(update: ZCodeToolUpdate, finished: boolean, parsed: ParsedMessageContent | undefined): string {
  const outcome = retainedOutcome(parsed?.completion)
  if (outcome === 'interrupted')
    return 'cancelled'
  if (update.isError || outcome === 'failed')
    return 'failed'
  return finished ? 'completed' : 'in_progress'
}

/**
 * The tool INPUT of one row, with the file path recovered from the result display.
 *
 * A ZCode file tool that omits its path from the arguments still states it in the
 * display hint of its result, and the shared title reads the input alone.
 */
function zcodeResolvedInput(kind: ToolKind, row: ZCodeRow, input: Record<string, unknown>): Record<string, unknown> {
  if (!ZCODE_FILE_KINDS.has(kind) || pickFirstString(input, TOOL_FILE_PATH_KEYS))
    return input
  const filePath = zcodeFilePath(row)
  return filePath ? { ...input, filePath } : input
}

/** One resolved body, and whether a notice must state that the provider cut the output. */
interface ZCodeToolBody {
  body: ToolBodySource
  /** The row draws the notice itself. A body that carries the flag sets it there. */
  truncated: boolean
}

/**
 * The body of a call that has NOT finished.
 *
 * A checklist and a subagent launch both state their content before the call
 * returns: the list the model wrote, and the instruction the subagent received.
 * Every other kind waits for its result.
 */
function zcodeRequestBody(row: ZCodeRow, kind: ToolKind): ToolBodySource {
  if (kind === 'todo') {
    const items = zcodeTodoItemsFromInput(zcodeToolInput(row))
    if (items)
      return { type: 'todo', items }
  }
  return { type: 'text' }
}

/**
 * The body of a FINISHED call.
 *
 * The order states the precedence. A display hint the app-server sent wins, because
 * it is the app-server's own rendering of the result. A structured diff comes next,
 * then the per-tool extractors, and a call that no extractor recognizes keeps its
 * text.
 */
function zcodeResultBody(row: ZCodeRow, update: ZCodeToolUpdate, kind: ToolKind, input: Record<string, unknown>, text: string): ToolBodySource {
  const display = zcodeResultDisplay(row)
  if (display && (display.kind === 'mcp' || display.kind === 'status' || !update.isError)) {
    if (display.kind === 'mcp')
      return { type: 'mcp', source: { ...display.source, argsJson: display.source.argsJson || prettifyArgsJson(input) } }
    if (display.kind === 'status') {
      return { type: 'status', source: {
        title: display.title,
        outcome: display.status === 'success' ? 'succeeded' : display.status,
        command: display.command,
        output: display.output,
      } }
    }
    // A node-image display carries its pictures beside the text, and the shared image
    // list draws them from `ToolMessageSource.images`.
    return { type: 'text' }
  }
  const diff = extractZCodeFileDiff(row)
  if (diff)
    return { type: 'diff', sources: [diff] }
  const command = extractZCodeBash(row)
  if (command)
    return { type: 'command', source: zcodeBashToCommandSource(command) }
  if (kind === 'agent') {
    const source = zcodeAgentResult(row)
    if (source)
      return { type: 'agent', source }
  }
  if (update.isError)
    return { type: 'text' }
  if (kind === 'todo') {
    const items = zcodeTodoItemsFromInput(input)
    if (items)
      return { type: 'todo', items }
  }
  const read = extractZCodeRead(row)
  if (read)
    return { type: 'read', source: read.source }
  const search = extractZCodeSearch(row)
  if (search)
    return { type: 'search', source: search }
  if (kind === 'fetch')
    return { type: 'fetch', source: { result: text, url: pickString(input, 'url'), durationMs: update.durationMs ?? undefined } }
  return { type: 'text' }
}

/**
 * Fold the provider's truncation flag into the body that states it.
 *
 * A command and a search each carry the flag on their own source, and their bodies
 * draw the notice in their own place. Every other body leaves it to the row.
 *
 * A rich-content body is the exception, and it keeps NO flag: that body replaces the
 * whole row with the shared Model Context Protocol card, which draws no part of the
 * presentation around it. A flag set here would promise a notice that no reader sees.
 */
function zcodeTruncatedBody(body: ToolBodySource, truncated: boolean): ZCodeToolBody {
  if (!truncated || body.type === 'mcp')
    return { body, truncated: false }
  if (body.type === 'command')
    return { body: { type: 'command', source: { ...body.source, truncated: true } }, truncated: false }
  if (body.type === 'search')
    return { body: { type: 'search', source: { ...body.source, truncated: true } }, truncated: false }
  return { body, truncated: true }
}

/** The display model of one ZCode tool call. */
export function zcodeToolPresentation(row: ZCodeRow, parsed?: ParsedMessageContent): ToolPresentation | null {
  const update = zcodeExtractTool(row.parsed)
  if (!update)
    return null
  const finished = zcodeToolFinished(update, parsed)
  const kind = zcodeToolKind(row.toolName)
  const input = zcodeResolvedInput(kind, row, zcodeToolInput(row))
  const text = update.isError ? zcodeErrorText(update) || 'Tool call failed' : update.result?.content ?? ''
  const truncated = update.result?.display?.truncated === true || update.result?.truncated === true
  const resolved = zcodeTruncatedBody(
    finished ? zcodeResultBody(row, update, kind, input, text) : zcodeRequestBody(row, kind),
    finished && truncated,
  )
  const presentation: ToolPresentation = {
    kind,
    // The tool's OWN name, which the icon tooltip states. A row that states none
    // falls back to the kind's word, because a name invented here is not one the
    // agent reported.
    label: row.toolName || undefined,
    // A command with no description of its own states the command, which the shared
    // header draws. Falling back to the tool name would put `Bash` above the very
    // command it ran.
    title: kind === 'execute'
      ? pickString(input, 'description')
      : pickString(input, 'description') || row.toolName || 'Tool',
    input,
    // The text the BODY shows. A read draws its parsed lines, so the row copies those
    // rather than the `cat -n` prefixes the app-server sent them with.
    output: resolved.body.type === 'read' ? readFileBodyText(resolved.body.source) : text,
    body: resolved.body,
    truncated: resolved.truncated || undefined,
    unresolvedTerminals: [],
  }
  if (resolved.body.type === 'todo')
    Object.assign(presentation, todoToolBody(resolved.body.items))
  if (kind === 'agent') {
    presentation.agentRequest = {
      toolName: ZCODE_TOOL.Agent,
      description: pickString(input, 'description'),
      agentType: pickString(input, 'subagent_type'),
      prompt: pickString(input, 'prompt'),
    }
  }
  return presentation
}

/** One row, as the shared tool component reads it. Null for a row that is not a tool call. */
export function zcodeToolMessageSource(row: ZCodeRow, parsed?: ParsedMessageContent): ToolMessageSource | null {
  const update = zcodeExtractTool(row.parsed)
  const presentation = zcodeToolPresentation(row, parsed)
  if (!update || !presentation)
    return null
  const finished = zcodeToolFinished(update, parsed)
  return {
    id: update.toolCallId,
    role: finished ? 'result' : update.kind === ZCODE_TOOL_KIND.Scheduled ? 'request' : 'update',
    status: zcodeToolStatus(update, finished, parsed),
    presentation,
    images: zcodeToolResultImages(row),
  }
}
