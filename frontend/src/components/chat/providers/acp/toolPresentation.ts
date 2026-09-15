import type { SpanRole } from '../registry'
import type { ToolBodySource, ToolPresentation } from '~/components/chat/results/toolPresentation'
import type { ToolRowOutcome } from '~/components/chat/toolOutcomeLabel'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { TOOL_FILE_PATH_KEYS } from '~/components/chat/results/toolInputs'
import { toolKind } from '~/components/chat/results/toolKind'
import { prettifyArgsJson } from '~/lib/jsonFormat'
import { isObject, pickFirstString, pickObject, pickString } from '~/lib/jsonPick'
import { fileEditHasDiff } from '../../results/fileEditDiff'
import { mcpStatusFromToolStatus, parseMcpContentItem } from '../../results/mcpToolCall'
import { retainedOutcome, retainedRowIsFinal } from '../registry'
import { collectAcpToolText, flattenAcpContent } from './content'
import { acpExecuteFromToolCall } from './extractors/execute'
import { acpFileEditFromToolCallContent, acpFileEditFromToolCallRawInput } from './extractors/fileEdit'
import { acpReadFromToolCall } from './extractors/read'
import { acpSearchFromToolCall } from './extractors/search'
import { acpTerminalIds, acpTerminalResults } from './extractors/terminal'
import { acpWebFetchFromToolCall } from './extractors/webFetch'
import { unwrapACPResult } from './resultWrapper'

/**
 * Each provider keeps its native fields and tool semantics in its own adapter.
 *
 * `row` states where this row sits in its tool span, which a provider needs to place
 * something exactly once across the two rows of ONE call. The tool's own fields cannot do
 * that job: `resolveACPToolCall` merges the opener into the result, so both rows can
 * carry the same `sessionUpdate` and `status`, and neither `acpToolFinished` nor the
 * stored `sessionUpdate` separates them by the time an adapter runs.
 */
export interface ACPToolRow {
  /** Where the row sits in its span. `createMessageRenderSources` decides it by message id. */
  role?: SpanRole
  /** True when a completing row is resolved beside this one. */
  hasResult?: boolean
}

export type ACPToolAdapter = (tool: Record<string, unknown>, presentation: ToolPresentation, supplemental: Record<string, unknown> | undefined, row?: ACPToolRow) => ToolPresentation

/** Resolve historical results that omit fields from the matching request. */
export function resolveACPToolCall(tool: Record<string, unknown>, opener?: Record<string, unknown>): Record<string, unknown> {
  if (!opener || !tool.toolCallId || opener.toolCallId !== tool.toolCallId)
    return tool
  const fields = Object.fromEntries(Object.entries(tool).filter(([, value]) => value !== undefined && value !== null))
  const resolved = { ...opener, ...fields }
  const previousInput = pickObject(opener, 'rawInput')
  const currentInput = pickObject(tool, 'rawInput')
  if (previousInput && currentInput)
    resolved.rawInput = { ...previousInput, ...currentInput }
  return resolved
}

export function acpToolFinished(tool: Record<string, unknown>, completion?: MessageCompletion): boolean {
  return retainedRowIsFinal(completion)
    || tool.status === 'completed' || tool.status === 'failed' || tool.status === 'cancelled'
}

/** Request related data when it determines the target or whether the request still needs a body. */
export function acpToolNeedsResult(tool: Record<string, unknown>, adapter?: ACPToolAdapter, supplemental?: unknown): boolean {
  // The BASE build answers every field this reads. The terminal merge that follows it
  // rewrites `output`, `body` and `unresolvedTerminals` alone, and it costs a walk of
  // the supplemental terminals -- so stopping short of it saves that walk on every
  // execute row. The adapter still runs, because an adapter can change `kind`, `input`
  // and `requestedChanges`, which are the three fields this decision rests on.
  const model = acpToolBase(tool, adapter, supplemental).model
  if (model.kind === 'agent' || model.kind === 'todo')
    return true
  if (model.requestedChanges?.length)
    return false
  if (['read', 'edit', 'write', 'delete', 'move'].includes(model.kind))
    return !pickFirstString(model.input, TOOL_FILE_PATH_KEYS)
  if (model.kind === 'execute')
    return !pickString(model.input, 'command')
  if (['search', 'glob', 'grep'].includes(model.kind))
    return !pickString(model.input, 'pattern') && !pickString(model.input, 'query')
  if (model.kind === 'fetch')
    return !pickString(model.input, 'url')
  return Object.keys(model.input).length === 0
}

export function acpToolPresentation(tool: Record<string, unknown>, adapter?: ACPToolAdapter, supplemental?: unknown, completion?: MessageCompletion, row?: ACPToolRow): ToolPresentation {
  const base = acpToolBase(tool, adapter, supplemental, completion, row)
  // An adapter can change the kind, so the test reads the ADAPTED model rather than the
  // kind the native call declared.
  return base.model.kind === 'execute' ? mergeAcpTerminals(base) : base.model
}

/**
 * The ACP tool status that LeapMux's own outcome states, for the two outcomes a
 * provider frame cannot state itself.
 *
 * A retained row still reads as pending or running, so the outcome the worker recorded
 * replaces the status before anything derives a body or a header from it. `succeeded`
 * is absent on purpose: a call that finished carries its own `completed` status, and an
 * override here would claim one for a row whose provider never sent it.
 */
const ACP_STATUS_FOR_OUTCOME: Partial<Record<ToolRowOutcome, string>> = {
  interrupted: 'cancelled',
  failed: 'failed',
}

/** One call, after the shared build and this provider's adapter, before the terminal merge. */
interface ACPToolBase {
  /** The call, with LeapMux's outcome and the repaired input folded in. */
  tool: Record<string, unknown>
  /** The supplemental record that belongs to this call, or undefined when none does. */
  extra: Record<string, unknown> | undefined
  model: ToolPresentation
}

/**
 * Build the presentation of one call, up to and including this provider's adapter.
 *
 * This is every step except the terminal merge, which only an `execute` call needs.
 * `acpToolNeedsResult` stops here; `acpToolPresentation` continues.
 */
function acpToolBase(rawTool: Record<string, unknown>, adapter?: ACPToolAdapter, supplemental?: unknown, completion?: MessageCompletion, row?: ACPToolRow): ACPToolBase {
  const extra = acpSupplementalData(rawTool, supplemental)
  const outcome = retainedOutcome(completion)
  const overriddenStatus = outcome ? ACP_STATUS_FOR_OUTCOME[outcome] : undefined
  let tool = overriddenStatus ? { ...rawTool, status: overriddenStatus } : rawTool
  const kind = pickString(tool, 'kind')
  const input = acpToolInput(tool, kind)
  if (typeof tool.rawInput !== 'string' && tool.rawInput !== input)
    tool = { ...tool, rawInput: input }
  const body = acpToolBody(tool, kind)
  const presentation: ToolPresentation = {
    // `kind` is a wire value, and `ToolPresentation.kind` is a closed set. The
    // narrowing happens HERE, at the one place a raw kind enters the shared
    // display model, so every table that reads it stays exhaustive.
    kind: toolKind(kind),
    title: pickString(input, 'description') || pickString(tool, 'title') || kind || 'Tool',
    input,
    inputText: typeof tool.rawInput === 'string' ? tool.rawInput : undefined,
    output: collectAcpToolText(tool, { rawObjects: kind === 'other' || !kind }),
    body,
    unresolvedTerminals: acpTerminalIds(tool.content),
  }
  if (body.type !== 'diff' && (kind === 'edit' || kind === 'write')) {
    const requested = acpFileEditFromToolCallRawInput(kind, input)
    if (fileEditHasDiff(requested))
      presentation.requestedChanges = [{ ...requested, showLineNumbers: false }]
  }
  if (body.type === 'text' && (kind === 'other' || !kind)) {
    const blocks = flattenAcpContent(tool.content)
    presentation.body = {
      type: 'mcp',
      source: {
        server: '',
        tool: presentation.title,
        argsJson: prettifyArgsJson(input),
        content: blocks.length > 0 ? blocks.map(parseMcpContentItem) : presentation.output ? [{ type: 'text', text: presentation.output }] : [],
        status: mcpStatusFromToolStatus(tool.status),
      },
    }
  }
  const adapted = adapter ? adapter(tool, presentation, extra, row) : presentation
  // A diff body already draws the change that landed, so the REQUESTED change beside it
  // would draw the same edit twice.
  const model = adapted.body.type === 'diff' ? { ...adapted, requestedChanges: undefined } : adapted
  return { tool, extra, model }
}

/**
 * The tool INPUT of one call, with the file path recovered from `locations`.
 *
 * A file tool that states no path in its own arguments often lists the file it touched
 * under `locations` instead. Exactly ONE distinct path there is the call's target.
 * Several paths are an ambiguity that this refuses to resolve, because picking one
 * would show a file that the call may not have touched.
 */
function acpToolInput(tool: Record<string, unknown>, kind: string): Record<string, unknown> {
  const input = pickObject(tool, 'rawInput') ?? {}
  if (!['read', 'edit', 'write', 'delete'].includes(kind) || pickFirstString(input, TOOL_FILE_PATH_KEYS))
    return input
  const paths = Array.isArray(tool.locations)
    ? [...new Set(tool.locations.filter(isObject).map(location => pickString(location, 'path')).filter(Boolean))]
    : []
  return paths.length === 1 ? { ...input, filePath: paths[0] } : input
}

/**
 * The rich BODY of one call, or a plain `text` body when no extractor recognizes it.
 *
 * A diff that the call CONTENT carries wins, because it states what actually landed.
 * Every other rich body needs a finished call: a partial frame carries partial output,
 * and a reader cannot tell that apart from the complete answer. A cancelled `execute`
 * is the exception, and it still draws its command body -- the output that the command
 * produced before the stop is real.
 */
function acpToolBody(tool: Record<string, unknown>, kind: string): ToolBodySource {
  if (tool.status === 'completed') {
    const sources = Array.isArray(tool.content)
      ? tool.content.flatMap((entry) => {
          const source = acpFileEditFromToolCallContent([entry])
          return fileEditHasDiff(source) ? [source] : []
        })
      : []
    if (sources.length > 0)
      return { type: 'diff', sources }
  }
  if (!acpToolFinished(tool))
    return { type: 'text' }
  if (kind === 'execute') {
    const source = acpExecuteFromToolCall(tool)
    return source ? { type: 'command', source: { ...source, interrupted: tool.status === 'cancelled' } } : { type: 'text' }
  }
  if (tool.status !== 'completed')
    return { type: 'text' }
  if (kind === 'read') {
    const source = acpReadFromToolCall(tool)
    if (source?.lines !== null && source)
      return { type: 'read', source }
  }
  else if (kind === 'search') {
    const source = acpSearchFromToolCall(tool)
    if (source)
      return { type: 'search', source }
  }
  else if (kind === 'fetch') {
    const source = acpWebFetchFromToolCall(tool)
    if (source)
      return { type: 'fetch', source }
  }
  return { type: 'text' }
}

/**
 * Fold the terminals of an `execute` call into its presentation.
 *
 * ACP reports a command's output through a TERMINAL that the call refers to by id, not
 * in the call itself, and one call can hold several. One resolved terminal becomes the
 * command body; several become a `commands` list, each entry with its own label. An id
 * that resolves to nothing stays in `unresolvedTerminals`, which is what marks the
 * output as unavailable rather than empty.
 */
function mergeAcpTerminals({ tool, extra, model }: ACPToolBase): ToolPresentation {
  const terminals = acpTerminalResults(tool, extra)
  const result = { ...model, unresolvedTerminals: terminals.unresolved }
  if (terminals.entries.length === 0) {
    return result.body.type === 'command' && terminals.unresolved.length > 0
      ? { ...result, body: { type: 'command', source: { ...result.body.source, outputUnavailable: true } } }
      : result
  }
  const output = terminals.entries.map(entry => entry.source.output).filter(Boolean)
  if (model.output && !output.includes(model.output))
    output.unshift(model.output)
  if (terminals.entries.length === 1 && terminals.unresolved.length === 0) {
    return { ...result, output: output.join('\n\n'), body: { type: 'command', source: {
      ...(model.body.type === 'command' ? model.body.source : {}),
      ...terminals.entries[0].source,
      output: output.join('\n\n'),
      isError: tool.status === 'failed' || terminals.entries[0].source.isError,
    } } }
  }
  const entries = model.output && !terminals.entries.some(entry => entry.source.output === model.output)
    ? [{ source: { output: model.output, isError: tool.status === 'failed' } }, ...terminals.entries]
    : terminals.entries
  return { ...result, output: entries.map(entry => [entry.label, entry.source.output].filter(Boolean).join('\n')).join('\n\n'), body: { type: 'commands', entries } }
}

export function parsedACPToolCall(parsed: unknown): Record<string, unknown> | null {
  return isObject(parsed) && (parsed.sessionUpdate === 'tool_call' || parsed.sessionUpdate === 'tool_call_update')
    ? parsed
    : null
}

/** Supplemental snapshots cannot change the message identity or its completion state. */
export function resolveACPMessage(parsed: ParsedMessageContent): Record<string, unknown> | undefined {
  const original = unwrapACPResult(parsed.parentObject)
  const supplemental = original ? acpSupplementalData(original, parsed.supplementalContent) : undefined
  if (!original || !supplemental)
    return original
  const resolved = { ...pickObject(supplemental, 'protocol'), ...original }
  // These fields come from later ACP request updates. Native records stay in supplemental content.
  for (const key of ['title', 'kind', 'rawInput', 'locations']) {
    if (key in supplemental)
      resolved[key] = supplemental[key]
  }
  const originalInput = pickObject(original, 'rawInput')
  const supplementalInput = pickObject(supplemental, 'rawInput')
  if (originalInput && supplementalInput)
    resolved.rawInput = { ...originalInput, ...supplementalInput }
  return resolved
}

function acpSupplementalData(original: Record<string, unknown>, supplemental: unknown): Record<string, unknown> | undefined {
  if (!original || !isObject(supplemental) || !original.toolCallId
    || supplemental.toolCallId !== original.toolCallId
    || supplemental.sessionUpdate !== original.sessionUpdate
    || supplemental.status !== original.status) {
    return undefined
  }
  return supplemental
}
