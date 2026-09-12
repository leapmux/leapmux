import type { ACPToolAdapter } from '../acp/toolPresentation'
import { CURSOR_TOOL } from '~/generated/contracts/cursor-protocol'
import { prettifyArgsJson } from '~/lib/jsonFormat'
import { isObject, pickBoolean, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { commandIsError } from '../../results/commandResult'
import { mcpToolCallDisplayName } from '../../results/mcpToolCall'
import { readFileSourceFromContent } from '../../results/readFileResult'
import { acpToolFinished } from '../acp/toolPresentation'
import { cursorAgentPresentation } from './agentResult'
import { cursorStoredToolPresentation } from './storedTool'

/** Cursor returns file and shell output in rawOutput without ACP content blocks. */
export const cursorToolAdapter: ACPToolAdapter = (tool, presentation, supplemental) => {
  const restored = cursorStoredToolPresentation(tool, presentation, supplemental)
  if (restored && restored.body !== presentation.body)
    return restored
  const raw = pickObject(tool, 'rawOutput')
  const input = restored?.input ?? presentation.input
  let model = restored ?? presentation
  if (input._toolName === CURSOR_TOOL.Task)
    return cursorAgentPresentation(tool, model)
  if (model.body.type === 'mcp' && pickString(input, 'toolName') && pickString(input, 'providerIdentifier')) {
    const source = { ...model.body.source, server: pickString(input, 'providerIdentifier'), tool: pickString(input, 'toolName'), argsJson: prettifyArgsJson(input.args) }
    model = { ...model, input: pickObject(input, 'args') ?? {}, title: mcpToolCallDisplayName(source), label: 'MCP Tool Call', body: { type: 'mcp', source } }
  }
  if (model.kind === 'execute')
    model = { ...model, title: pickString(input, 'description') }
  if (model.kind === 'search') {
    const title = pickString(tool, 'title')
    if (title === 'Find' || title.startsWith('Find `'))
      model = { ...model, kind: 'glob' }
    else if (title === 'grep' || title.startsWith('grep "'))
      model = { ...model, kind: 'grep' }
  }
  if (presentation.body.type === 'diff') {
    model = {
      ...model,
      body: {
        type: 'diff',
        sources: presentation.body.sources.map((source) => {
          // Cursor's fallback diff parser removes one prefix character from file headers.
          const header = `++ b/${source.filePath}\n`
          return source.oldStr === '-- /dev/null' && source.newStr.startsWith(header)
            ? { ...source, oldStr: '', newStr: source.newStr.slice(header.length) }
            : source
        }),
      },
    }
  }
  if (model.kind === 'execute' && acpToolFinished(tool) && raw) {
    const stdout = pickString(raw, 'stdout')
    const stderr = pickString(raw, 'stderr')
    const output = [stdout, stderr].filter(Boolean).join(stdout.endsWith('\n') ? '' : '\n') || model.output
    const exitCode = pickNumber(raw, 'exitCode', undefined)
    return {
      ...model,
      output,
      body: { type: 'command', source: { output, stderr, exitCode, isError: commandIsError(pickString(tool, 'status'), exitCode), interrupted: tool.status === 'cancelled' } },
    }
  }
  if (model.kind === 'read' && tool.status === 'completed' && typeof raw?.content === 'string') {
    const location = Array.isArray(tool.locations) ? tool.locations.find(isObject) : undefined
    const reportedLine = pickNumber(location, 'line', undefined)
    const startLine = reportedLine !== undefined && Number.isSafeInteger(reportedLine) && reportedLine > 0 ? reportedLine : 1
    return {
      ...model,
      input,
      output: raw.content,
      body: { type: 'read', source: readFileSourceFromContent({ filePath: pickString(input, 'path'), content: raw.content, startLine }) },
    }
  }
  if (['search', 'glob', 'grep'].includes(model.kind) && tool.status === 'completed' && raw) {
    const totalFiles = pickNumber(raw, 'totalFiles', undefined)
    const matches = pickNumber(raw, 'totalMatches', undefined) ?? pickNumber(raw, 'resultCount', undefined)
    if (totalFiles !== undefined || matches !== undefined) {
      return {
        ...model,
        kind: totalFiles !== undefined ? 'glob' : 'grep',
        body: {
          type: 'search',
          source: {
            variant: totalFiles !== undefined ? 'glob' : 'search',
            filenames: [],
            content: model.output,
            numFiles: totalFiles ?? 0,
            numLines: 0,
            matches,
            truncated: pickBoolean(raw, 'truncated') ?? false,
            fallbackContent: model.output,
          },
        },
      }
    }
  }
  const error = pickString(raw, 'error')
  return error && !model.output ? { ...model, output: error } : model
}
