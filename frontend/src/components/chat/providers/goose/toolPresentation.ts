import type { ACPToolAdapter } from '../acp/toolPresentation'
import type { ToolPresentation } from '~/components/chat/results/toolPresentation'
import { prettifyJson } from '~/lib/jsonFormat'
import { pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { rawTodosToItems } from '~/stores/chatTodos'
import { agentToolPresentation } from '../../results/AgentRequestMessage'
import { commandIsError } from '../../results/commandResult'
import { splitExitCodeMarker } from '../../results/exitCodeMarker'
import { mcpStatusFromToolStatus, mcpToolCallDisplayName, parseMcpContentItem } from '../../results/mcpToolCall'
import { readFileSourceFromContent } from '../../results/readFileResult'
import { todoToolBody } from '../../results/toolPresentation'
import { flattenAcpContent } from '../acp/content'
import { acpToolFinished, acpToolPresentation } from '../acp/toolPresentation'
import { gooseAgentRequest, gooseAgentResult } from './agentResult'
import { gooseSubagentToolCall, isGooseSubagentToolRequest } from './subagentToolRequest'

/** Goose puts stable tool identity in metadata. Its generated title can change. */
export const gooseToolAdapter: ACPToolAdapter = (tool, initial) => {
  if (isGooseSubagentToolRequest(tool)) {
    const requested = gooseSubagentToolCall(tool)
    const name = pickString(requested, 'name') || 'tool'
    const request = {
      ...tool,
      status: 'pending',
      kind: 'other',
      title: `Requested tool: ${name}`,
      rawInput: pickObject(requested, 'arguments') ?? {},
      content: [],
      rawOutput: undefined,
      _meta: { goose: { toolCall: { toolName: name } } },
    }
    return { ...nativeGooseToolPresentation(request, acpToolPresentation(request)), label: 'Tool request' }
  }
  return nativeGooseToolPresentation(tool, initial)
}

function nativeGooseToolPresentation(tool: Record<string, unknown>, initial: ToolPresentation): ToolPresentation {
  const metadata = pickObject(pickObject(pickObject(tool, '_meta'), 'goose'), 'toolCall')
  const fullName = pickString(metadata, 'toolName')
  const separator = fullName.indexOf('__')
  const extension = pickString(metadata, 'extensionName') || (separator >= 0 ? fullName.slice(0, separator) : '')
  const name = separator >= 0 ? fullName.slice(separator + 2) : fullName
  if (extension === 'summon' && name === 'delegate') {
    return agentToolPresentation(
      initial,
      gooseAgentRequest(initial.input),
      acpToolFinished(tool) ? gooseAgentResult(initial.input, initial.output, tool.status) : undefined,
    )
  }
  if (extension === 'todo' && name === 'todo_write' && typeof initial.input.content === 'string' && tool.status !== 'failed' && tool.status !== 'cancelled') {
    const markdown = pickString(initial.input, 'content')
    const lines = markdown.split(/\r?\n/).filter(line => line.trim() !== '')
    const entries = lines.map(line => /^[-*+] \[([ x])\] (.+)$/i.exec(line))
    if (entries.every(entry => entry !== null)) {
      const items = rawTodosToItems(entries.map(entry => ({ content: entry![2], status: entry![1].toLowerCase() === 'x' ? 'completed' : 'pending' })))
      return { ...initial, ...todoToolBody(items) }
    }
    return { ...initial, kind: 'todo', title: 'To-do list', body: { type: 'markdown', text: markdown } }
  }
  if (extension === 'developer') {
    const kind = pickString({ shell: 'execute', edit: 'edit', write: 'write', read: 'read', read_image: 'read', tree: 'search' }, name)
    if (!kind)
      return initial
    const input = { ...initial.input }
    if (name === 'edit') {
      input.oldText = input.before
      input.newText = input.after
    }
    if (name === 'read')
      input.offset = input.line
    const model = acpToolPresentation({ ...tool, kind, rawInput: input })
    if (name === 'shell')
      model.title = pickString(input, 'description')
    if (name === 'shell' && acpToolFinished(tool)) {
      const raw = pickObject(tool, 'rawOutput')
      const stdout = pickString(raw, 'stdout', undefined)
      const stderr = pickString(raw, 'stderr', undefined)
      const output = stdout !== undefined || stderr !== undefined
        ? [stdout, stderr].filter(value => value !== undefined && value !== '').join(stdout?.endsWith('\n') ? '' : '\n')
        : model.output
      // A failed shell call carries no `rawOutput` at all. Goose states the code in
      // a content block of its own, ahead of the output: `exit code: 1`. That block
      // is the only statement of it, so without reading it the row says "Error"
      // where every other provider says "Error (exit 1)".
      const marked = splitExitCodeMarker(output)
      const rawExit = pickNumber(raw, 'exit_code', undefined)
      // The line is consumed only when it states the code being reported. A
      // disagreement with `rawOutput.exit_code` is worth showing, so it survives one.
      const consume = marked.exitCode !== undefined && (rawExit === undefined || rawExit === marked.exitCode)
      const exitCode = rawExit ?? marked.exitCode
      const shown = consume ? marked.output : output
      return { ...model, output: shown, body: { type: 'command', source: { output: shown, stderr, exitCode, isError: commandIsError(pickString(tool, 'status'), exitCode), interrupted: tool.status === 'cancelled' } } }
    }
    if (name === 'read' && tool.status === 'completed') {
      return { ...model, body: { type: 'read', source: readFileSourceFromContent({ filePath: pickString(input, 'path'), content: model.output, startLine: pickNumber(input, 'line', undefined) }) } }
    }
    // Tree output contains branches and line counts. Keep that structure intact.
    if (name === 'tree')
      return { ...model, label: 'List Files', body: { type: 'text' } }
    return model
  }
  if (!extension || !name || extension === 'todo')
    return initial
  const source = {
    server: extension,
    tool: name,
    argsJson: Object.keys(initial.input).length > 0 ? prettifyJson(initial.input) : '',
    content: flattenAcpContent(tool.content).map(parseMcpContentItem),
    structuredJson: tool.rawOutput !== undefined ? prettifyJson(tool.rawOutput) : undefined,
    status: mcpStatusFromToolStatus(tool.status),
  }
  return { ...initial, kind: 'other', label: 'MCP Tool Call', title: mcpToolCallDisplayName(source), body: { type: 'mcp', source } }
}
