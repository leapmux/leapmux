import type { ACPToolAdapter } from '../acp/toolPresentation'
import type { ToolPresentation } from '~/components/chat/results/toolPresentation'
import { REASONIX_CAPABILITY_ACTION, REASONIX_CAPABILITY_PREFIX, REASONIX_TOOL, REASONIX_TOOL_RECORD } from '~/generated/contracts/reasonix-protocol'
import { prettifyJson } from '~/lib/jsonFormat'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { rawTodosToItems } from '~/stores/chatTodos'
import { parseUnifiedDiffCached } from '../../diff'
import { agentToolPresentation } from '../../results/AgentRequestMessage'
import { fileEditDiffFromHunks, fileEditHasDiff } from '../../results/fileEditDiff'
import { mcpStatusFromToolStatus, mcpToolCallDisplayName, parseMcpContentItem, parseMcpToolName, splitPrefixedPair } from '../../results/mcpToolCall'
import { todoToolBody } from '../../results/toolPresentation'
import { collectAcpToolText, flattenAcpContent } from '../acp/content'
import { acpToolFinished, acpToolPresentation } from '../acp/toolPresentation'
import { reasonixAgentResult } from './agentResult'
import { reasonixDirectoryOutput } from './directoryOutput'
import { reasonixEditReceipt } from './editReceipt'

/**
 * The capability prefix that carries a Model Context Protocol tool.
 *
 * Not in `contracts/reasonix-protocol.json`, which holds the identifiers BOTH programs
 * read. The worker never splits this one, so the contract rule keeps it on the side
 * that does.
 */
const REASONIX_MCP_CAPABILITY_PREFIX = 'mcp-tool:'

const toolKinds: Record<string, string> = {
  read_file: 'read',
  view_image: 'read',
  glob: 'search',
  grep: 'search',
  ls: 'list',
  edit_file: 'edit',
  multi_edit: 'edit',
  write_file: 'write',
  move_file: 'move',
  delete_range: 'delete',
  delete_symbol: 'delete',
  bash: 'execute',
  web_fetch: 'fetch',
}

function searchResult(name: string, model: ToolPresentation): ToolPresentation {
  const output = model.output.trim()
  const empty = output === '(no matches)' || output === ''
  const lines = empty ? [] : output.split('\n')
  const truncated = lines.some(line => /^\.\.\. \((?:truncated|timed out)/.test(line))
  if (name === 'glob') {
    const filenames = lines.filter(line => line !== '' && !/^\.\.\. \(/.test(line))
    return { ...model, body: { type: 'search', source: { variant: 'glob', filenames, content: '', numFiles: filenames.length, numLines: 0, truncated, fallbackContent: empty ? '' : output } } }
  }
  const matches = lines.filter(line => /^.+:\d+:/.test(line))
  return { ...model, body: { type: 'search', source: { variant: 'search', filenames: [], content: model.output, numFiles: 0, numLines: matches.length, matches: matches.length || (empty ? 0 : undefined), truncated, fallbackContent: empty ? '' : model.output } } }
}

/** Unwrap a capability call before choosing the normal tool components. */
export const reasonixToolAdapter: ACPToolAdapter = (tool, initial, supplemental) => {
  // The worker wraps Reasonix's own transcript record under this envelope key, and
  // both sides read the record's field names from contracts/reasonix-protocol.json.
  // A Go struct tag takes a literal, so TestReasonixToolRecordTagsMatchTheContract
  // pins the worker's tags to the same table.
  const stored = pickObject(pickObject(supplemental, 'rawOutput'), REASONIX_TOOL_RECORD.Envelope)
  const saved = stored?.[REASONIX_TOOL_RECORD.RoleField] === REASONIX_TOOL_RECORD.ToolRole && stored[REASONIX_TOOL_RECORD.ToolCallIDField] === tool.toolCallId ? stored : undefined
  const savedText = pickString(saved, REASONIX_TOOL_RECORD.RawContentField, undefined) || pickString(saved, REASONIX_TOOL_RECORD.ContentField, undefined)
  const readResult = pickObject(saved, 'read_result')
  const filePath = pickString(pickObject(readResult, 'source'), 'canonical_path')
  const resolvedTool = savedText !== undefined
    ? { ...tool, content: [{ type: 'content', content: { type: 'text', text: savedText } }] }
    : tool
  let name = pickString(tool, 'title')
  let input = initial.input
  let mcp: { server: string, tool: string } | undefined
  if (name === REASONIX_TOOL.UseCapability && input.action === REASONIX_CAPABILITY_ACTION.Call) {
    const id = pickString(input, 'capability_id')
    const args = pickObject(input, 'arguments')
    if (args && id.startsWith(REASONIX_CAPABILITY_PREFIX.Tool)) {
      name = id.slice(REASONIX_CAPABILITY_PREFIX.Tool.length)
      input = args
    }
    else if (args) {
      const pair = splitPrefixedPair(id, REASONIX_MCP_CAPABILITY_PREFIX, '/')
      if (pair) {
        mcp = pair
        input = args
      }
    }
  }
  else {
    mcp = parseMcpToolName(name) ?? undefined
  }
  if (mcp) {
    const source = {
      ...mcp,
      argsJson: Object.keys(input).length > 0 ? prettifyJson(input) : '',
      content: flattenAcpContent(resolvedTool.content).map(parseMcpContentItem),
      status: mcpStatusFromToolStatus(tool.status),
    }
    return { ...initial, input, kind: 'other', label: 'MCP Tool Call', title: mcpToolCallDisplayName(source), body: { type: 'mcp', source } }
  }
  name ||= pickString(saved, REASONIX_TOOL_RECORD.NameField)
  if (name === 'todo_write' && Array.isArray(input.todos) && tool.status !== 'failed' && tool.status !== 'cancelled') {
    const items = rawTodosToItems(input.todos)
    return { ...initial, input, ...todoToolBody(items) }
  }
  if (name === 'read_file' && filePath)
    input = { ...input, path: filePath }
  const model = acpToolPresentation({ ...resolvedTool, title: name, kind: pickString(toolKinds, name) || tool.kind, rawInput: input })
  if (name === REASONIX_TOOL.Task || name === REASONIX_TOOL.ReadOnlyTask) {
    return agentToolPresentation(
      model,
      { toolName: 'Task', description: pickString(input, 'description'), agentType: pickString(input, 'profile'), prompt: pickString(input, 'prompt') },
      acpToolFinished(tool)
        ? reasonixAgentResult({ toolName: name, input, output: model.output, originalOutput: collectAcpToolText(tool, { rawObjects: false }), status: tool.status })
        : undefined,
    )
  }
  if (name === 'bash')
    model.title = pickString(input, 'description')
  if (name === 'glob' || name === 'grep')
    model.kind = name
  if (name === 'ls' && tool.status === 'completed')
    return { ...model, body: { type: 'directory', source: reasonixDirectoryOutput(model.output) } }
  if (tool.status === 'completed' && (name === 'delete_range' || name === 'delete_symbol')) {
    const patch = parseUnifiedDiffCached(model.output)
    if (patch)
      return { ...model, body: { type: 'diff', sources: [fileEditDiffFromHunks(pickString(input, 'path'), patch.hunks)] } }
  }
  if (tool.status === 'completed' && (name === 'edit_file' || name === 'multi_edit')) {
    const path = pickString(input, 'path')
    const receipt = reasonixEditReceipt(model.output, path, input)
    if (receipt !== null)
      return { ...model, body: receipt.length ? { type: 'diff', sources: receipt } : { type: 'text' } }
    if (name === 'multi_edit' && Array.isArray(input.edits)) {
      const sources = input.edits.flatMap(edit => isObject(edit) && typeof edit.old_string === 'string' && typeof edit.new_string === 'string'
        ? [{ filePath: path, structuredPatch: null, oldStr: edit.old_string, newStr: edit.new_string, showLineNumbers: false }]
        : []).filter(fileEditHasDiff)
      if (sources.length)
        return { ...model, requestedChanges: sources, body: { type: 'text' } }
    }
  }
  if (name === 'move_file' && tool.status === 'completed') {
    const previousPath = pickString(input, 'source_path')
    const filePath = pickString(input, 'destination_path')
    if (previousPath && filePath && previousPath !== filePath)
      return { ...model, kind: 'edit', body: { type: 'diff', sources: [{ filePath, previousPath, operation: 'move', oldStr: '', newStr: '', structuredPatch: null }] } }
  }
  if (model.body.type === 'read' && readResult?.eof === true)
    model.body.source.totalLines = pickNumber(readResult, 'source_end', 0)
  if (tool.status === 'completed' && (name === 'glob' || name === 'grep'))
    return searchResult(name, model)
  if (name === 'ls')
    return { ...model, label: 'List Files', body: { type: 'text' } }
  return model
}
