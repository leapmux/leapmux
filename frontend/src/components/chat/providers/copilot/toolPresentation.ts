import type { ACPToolAdapter } from '../acp/toolPresentation'
import { COPILOT_TOOL } from '~/generated/contracts/copilot-protocol'
import { prettifyArgsJson, prettifyStructuredJson } from '~/lib/jsonFormat'
import { pickObject, pickString } from '~/lib/jsonPick'
import { commandIsError } from '../../results/commandResult'
import { parseMcpContentItem } from '../../results/mcpToolCall'
import { readFileSourceFromContent } from '../../results/readFileResult'
import { acpInputPaths } from '../acp/content'
import { acpToolFinished } from '../acp/toolPresentation'
import { copilotAgentResult } from './agentResult'
import { copilotNativeTool } from './nativeTool'
import { copilotPatchRequest } from './patchRequest'

const SHELL_COMPLETION = /(?:\r?\n)?<shellId: [^\r\n>]+ completed with exit code (-?\d+)>\s*$/

/** Copilot keeps view output and shell status in its native result object. */
export const copilotToolAdapter: ACPToolAdapter = (tool, presentation, supplemental) => {
  const native = copilotNativeTool(tool, supplemental)
  const nativeResult = pickObject(native?.completed, 'data')
  const raw = pickObject(tool, 'rawOutput') ?? (acpToolFinished(tool) ? pickObject(nativeResult, 'result') ?? pickObject(nativeResult, 'error') : null)
  const output = pickString(raw, 'content', undefined) ?? pickString(raw, 'message', undefined) ?? presentation.output
  const input = { ...pickObject(native?.request, 'arguments'), ...presentation.input }
  if (native?.name === COPILOT_TOOL.Task) {
    return {
      ...presentation,
      input,
      output,
      kind: 'agent',
      title: pickString(input, 'description') || 'Agent',
      agentRequest: { toolName: 'Task', description: pickString(input, 'description'), agentType: pickString(input, 'agent_type'), prompt: pickString(input, 'prompt') },
      body: acpToolFinished(tool) ? { type: 'agent', source: copilotAgentResult(native, input, output, tool.status) } : { type: 'text' },
    }
  }
  const range = input.view_range
  if (presentation.kind === 'read' && Array.isArray(range)) {
    const [first, last] = range
    if (typeof first === 'number' && Number.isSafeInteger(first) && first > 0) {
      input.offset = first
      if (typeof last === 'number' && Number.isSafeInteger(last) && last >= first)
        input.limit = last - first + 1
    }
  }
  const requestedChanges = presentation.kind === 'edit' && typeof tool.rawInput === 'string' ? copilotPatchRequest(tool.rawInput) : null
  const operation = requestedChanges?.length === 1 ? requestedChanges[0] : undefined
  if (operation) {
    input.path = operation.filePath
    if (operation.operation === 'add')
      input.content = operation.newStr
  }
  const model = {
    ...presentation,
    input,
    output,
    kind: operation?.operation === 'add' ? 'write' : operation?.operation === 'delete' ? 'delete' : presentation.kind,
    requestedChanges: requestedChanges ?? undefined,
    inputText: requestedChanges ? undefined : presentation.inputText,
  }
  if (Array.isArray(raw?.contents)) {
    return {
      ...model,
      body: {
        type: 'mcp',
        source: {
          server: '',
          tool: model.title,
          argsJson: prettifyArgsJson(model.input),
          content: raw.contents.map(parseMcpContentItem),
          structuredJson: prettifyStructuredJson(raw.structuredContent),
          status: tool.status === 'failed' || tool.status === 'cancelled' ? 'failed' : tool.status === 'completed' ? 'completed' : 'inProgress',
        },
      },
    }
  }
  const title = pickString(tool, 'title')
  const glob = (native?.name === COPILOT_TOOL.Glob || (!native && title.startsWith('Finding files matching '))) && typeof model.input.pattern === 'string'
  const grep = (native?.name === COPILOT_TOOL.Grep || (!native && title.startsWith('Searching for '))) && typeof model.input.pattern === 'string'
  if (glob || grep) {
    const paths = acpInputPaths(model.input)
    const input = { ...model.input, path: paths.length === 1 ? paths[0] : undefined }
    if (tool.status !== 'completed')
      return { ...model, kind: glob ? 'glob' : 'grep', input, body: { type: 'text' } }
    const empty = output.trim() === '' || /^No (?:files|matches) (?:found|matched)[.!]?$/i.test(output.trim())
    const lines = empty ? [] : output.trim().split('\n').filter(Boolean)
    const fileList = glob || model.input.output_mode === 'files_with_matches'
    const countMode = model.input.output_mode === 'count'
    const counts = countMode ? lines.map(line => line.match(/:(\d+)\s*$/)).filter(match => match !== null) : []
    const hasContext = ['A', 'B', 'C', '-A', '-B', '-C', 'context', 'after_context', 'before_context'].some(key => model.input[key] !== undefined && model.input[key] !== 0)
    return {
      ...model,
      kind: glob ? 'glob' : 'grep',
      label: glob ? 'Glob' : 'Grep',
      input,
      body: { type: 'search', source: {
        variant: fileList ? 'glob' : countMode ? 'grep' : 'search',
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
  if (model.kind === 'execute' && acpToolFinished(tool)) {
    const trailer = output.match(SHELL_COMPLETION)
    const exitCode = trailer ? Number(trailer[1]) : undefined
    const text = trailer ? output.slice(0, trailer.index) : output
    return {
      ...model,
      output: text,
      body: { type: 'command', source: { output: text, exitCode, isError: commandIsError(pickString(tool, 'status'), exitCode), interrupted: tool.status === 'cancelled' } },
    }
  }
  if (model.kind === 'read' && tool.status === 'completed' && typeof raw?.content === 'string' && pickString(model.input, 'path')) {
    const range = model.input.view_range
    const first = Array.isArray(range) ? range[0] : undefined
    const startLine = typeof first === 'number' && Number.isSafeInteger(first) && first > 0 ? first : 1
    return {
      ...model,
      body: { type: 'read', source: readFileSourceFromContent({ filePath: pickString(model.input, 'path'), content: raw.content, startLine }) },
    }
  }
  return model
}
