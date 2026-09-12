import type { ToolBodySource, ToolPresentation } from '~/components/chat/results/toolPresentation'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { prettifyArgsJson } from '~/lib/jsonFormat'
import { isObject, pickFirstString, pickObject, pickString } from '~/lib/jsonPick'
import { messageCompletionFromProto } from '../../assembledMessage'
import { fileEditHasDiff } from '../../results/fileEditDiff'
import { parseMcpContentItem } from '../../results/mcpToolCall'
import { ACP_FILE_PATH_KEYS, collectAcpToolText, flattenAcpContent } from './content'
import { acpExecuteFromToolCall } from './extractors/execute'
import { acpFileEditFromToolCallContent, acpFileEditFromToolCallRawInput } from './extractors/fileEdit'
import { acpReadFromToolCall } from './extractors/read'
import { acpSearchFromToolCall } from './extractors/search'
import { acpTerminalIds, acpTerminalResults } from './extractors/terminal'
import { acpWebFetchFromToolCall } from './extractors/webFetch'

/** Each provider keeps its native fields and tool semantics in its own adapter. */
export type ACPToolAdapter = (tool: Record<string, unknown>, presentation: ToolPresentation, supplemental: Record<string, unknown> | undefined) => ToolPresentation

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
  return messageCompletionFromProto(completion) !== null || tool.status === 'completed' || tool.status === 'failed' || tool.status === 'cancelled'
}

/** Request related data when it determines the target or whether the request still needs a body. */
export function acpToolNeedsResult(tool: Record<string, unknown>, adapter?: ACPToolAdapter, supplemental?: unknown): boolean {
  const model = acpToolPresentation(tool, adapter, supplemental)
  if (model.kind === 'agent' || model.kind === 'todo')
    return true
  if (model.requestedChanges?.length)
    return false
  if (['read', 'edit', 'write', 'delete', 'move'].includes(model.kind))
    return !pickFirstString(model.input, ACP_FILE_PATH_KEYS)
  if (model.kind === 'execute')
    return !pickString(model.input, 'command')
  if (['search', 'glob', 'grep'].includes(model.kind))
    return !pickString(model.input, 'pattern') && !pickString(model.input, 'query')
  if (model.kind === 'fetch')
    return !pickString(model.input, 'url')
  return Object.keys(model.input).length === 0
}

export function acpToolPresentation(tool: Record<string, unknown>, adapter?: ACPToolAdapter, supplemental?: unknown, completion?: MessageCompletion): ToolPresentation {
  const extra = acpSupplementalData(tool, supplemental)
  const retainedCompletion = messageCompletionFromProto(completion)
  if (retainedCompletion === 'interrupted' || retainedCompletion === 'error')
    tool = { ...tool, status: retainedCompletion === 'interrupted' ? 'cancelled' : 'failed' }
  const kind = pickString(tool, 'kind')
  let input = pickObject(tool, 'rawInput') ?? {}
  if (['read', 'edit', 'write', 'delete'].includes(kind) && !pickFirstString(input, ACP_FILE_PATH_KEYS)) {
    const paths = Array.isArray(tool.locations) ? [...new Set(tool.locations.filter(isObject).map(location => pickString(location, 'path')).filter(Boolean))] : []
    if (paths.length === 1)
      input = { ...input, filePath: paths[0] }
  }
  if (typeof tool.rawInput !== 'string' && tool.rawInput !== input)
    tool = { ...tool, rawInput: input }
  let body: ToolBodySource = { type: 'text' }
  if (tool.status === 'completed') {
    const sources = Array.isArray(tool.content)
      ? tool.content.flatMap((entry) => {
          const source = acpFileEditFromToolCallContent([entry])
          return fileEditHasDiff(source) ? [source] : []
        })
      : []
    if (sources.length === 0) {
      const fallback = acpFileEditFromToolCallRawInput(kind, input)
      if (fileEditHasDiff(fallback))
        sources.push(fallback)
    }
    if (sources.length > 0)
      body = { type: 'diff', sources }
  }
  if (body.type === 'text' && acpToolFinished(tool)) {
    if (kind === 'execute') {
      const source = acpExecuteFromToolCall(tool)
      if (source)
        body = { type: 'command', source: { ...source, interrupted: tool.status === 'cancelled' } }
    }
    else if (tool.status === 'completed') {
      if (kind === 'read') {
        const source = acpReadFromToolCall(tool)
        if (source?.lines !== null && source)
          body = { type: 'read', source }
      }
      else if (kind === 'search') {
        const source = acpSearchFromToolCall(tool)
        if (source)
          body = { type: 'search', source }
      }
      else if (kind === 'fetch') {
        const source = acpWebFetchFromToolCall(tool)
        if (source)
          body = { type: 'fetch', source }
      }
    }
  }
  const presentation: ToolPresentation = {
    kind,
    title: pickString(input, 'description') || pickString(tool, 'title') || kind || 'Tool',
    input,
    inputText: typeof tool.rawInput === 'string' ? tool.rawInput : undefined,
    output: collectAcpToolText(tool, { rawObjects: kind === 'other' || !kind }),
    body,
    unresolvedTerminals: acpTerminalIds(tool.content),
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
        status: tool.status === 'failed' || tool.status === 'cancelled' ? 'failed' : tool.status === 'completed' ? 'completed' : 'inProgress',
      },
    }
  }
  const model = adapter ? adapter(tool, presentation, extra) : presentation
  if (model.kind !== 'execute')
    return model
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
  const original = parsed.parentObject
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
