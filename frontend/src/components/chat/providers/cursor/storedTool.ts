import type { McpContentItem } from '../../results/mcpToolCall'
import type { SearchResultLine, SearchResultSource } from '../../results/searchResult'
import type { ToolPresentation } from '~/components/chat/results/toolPresentation'
import { prettifyArgsJson, prettifyJson } from '~/lib/jsonFormat'
import { isObject, pickBoolean, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { isAbsolute, join } from '~/lib/paths'
import { parseUnifiedDiffCached } from '../../diff'
import { commandIsError } from '../../results/commandResult'
import { fileEditDiffFromHunks, fileEditHasDiff } from '../../results/fileEditDiff'
import { mcpToolCallDisplayName, parseMcpContentItem } from '../../results/mcpToolCall'
import { readFileSourceFromContent } from '../../results/readFileResult'
import { cursorAgentPresentation } from './agentResult'

function fileInWorkspace(workspace: string, path: string): string {
  return isAbsolute(path) ? path : join([workspace, path.replace(/^\.[/\\]/, '')])
}

/** Cursor stores both standard content blocks and native protobuf content variants. */
function cursorMcpContent(saved: Record<string, unknown>, success: Record<string, unknown> | null, output: string): McpContentItem[] {
  const standard = Array.isArray(saved.experimental_content) ? saved.experimental_content : []
  const native = Array.isArray(success?.content) ? success.content : []
  const blocks = standard.length
    ? standard
    : native.map((entry) => {
        if (!isObject(entry))
          return entry
        const text = pickObject(entry, 'text')
        if (text)
          return { ...text, type: 'text' }
        const image = pickObject(entry, 'image')
        return image ? { ...image, type: 'image' } : entry
      })
  if (!blocks.length)
    return output ? [{ type: 'text', text: output }] : []
  const descriptions = pickObject(pickObject(pickObject(saved, 'providerOptions'), 'cursor'), 'imageDescriptions')
  return blocks.map((block, index) => {
    const item = parseMcpContentItem(block)
    const description = pickString(descriptions, String(index))
    return item.type === 'image' && description ? { ...item, source: { ...item.source, description } } : item
  })
}

function cursorSearchSource(success: Record<string, unknown>, model: ToolPresentation, glob: boolean): SearchResultSource {
  const filenames: string[] = []
  const lines: SearchResultLine[] = []
  const workspaces = pickObject(success, 'workspaceResults') ?? {}
  let numMatches = 0
  let truncated = success.clientTruncated === true
  for (const [workspace, raw] of Object.entries(workspaces)) {
    if (!isObject(raw))
      continue
    const content = pickObject(raw, 'content')
    if (Array.isArray(content?.matches)) {
      for (const match of content.matches) {
        if (!isObject(match))
          continue
        const path = fileInWorkspace(workspace, pickString(match, 'file'))
        if (Array.isArray(match.matches)) {
          for (const line of match.matches) {
            if (!isObject(line) || typeof line.content !== 'string')
              continue
            const number = pickNumber(line, 'lineNumber', undefined)
            lines.push({ filePath: path, lineNumber: number, text: line.content })
            numMatches++
          }
        }
        filenames.push(path)
      }
      truncated ||= content.ripgrepTruncated === true || content.clientTruncated === true
    }
    const files = pickObject(raw, 'files')
    if (Array.isArray(files?.files)) {
      filenames.push(...files.files.filter((file): file is string => typeof file === 'string').map(file => fileInWorkspace(workspace, file)))
      truncated ||= files.ripgrepTruncated === true || files.clientTruncated === true
    }
  }
  if (Array.isArray(success.files)) {
    const path = pickString(success, 'path')
    filenames.push(...success.files.filter((file): file is string => typeof file === 'string').map(file => fileInWorkspace(path, file)))
  }
  const uniqueFiles = [...new Set(filenames)]
  return {
    variant: glob ? 'glob' : 'grep',
    filenames: glob || lines.length === 0 ? uniqueFiles : [],
    content: lines.map(line => `${line.filePath}${line.lineNumber !== undefined ? `:${line.lineNumber}` : ''}:${line.text}`).join('\n'),
    lines,
    numFiles: uniqueFiles.length,
    numLines: numMatches,
    truncated,
    fallbackContent: model.output,
  }
}

/** Read only the saved result whose ID matches the current ACP tool call. */
export function cursorStoredToolPresentation(tool: Record<string, unknown>, model: ToolPresentation, supplemental: Record<string, unknown> | undefined): ToolPresentation | null {
  const raw = pickObject(supplemental, 'rawOutput')
  const protocolOutput = pickObject(tool, 'rawOutput')
  if (!Array.isArray(raw?.content) || !tool.toolCallId)
    return null
  const saved = raw.content.find(value => isObject(value) && value.type === 'tool-result' && value.toolCallId === tool.toolCallId)
  if (!isObject(saved))
    return null
  const native = pickObject(pickObject(pickObject(raw, 'providerOptions'), 'cursor'), 'highLevelToolCallResult')
  const success = pickObject(pickObject(native, 'output'), 'success')
  const name = pickString(saved, 'toolName')
  const output = typeof saved.result === 'string' ? saved.result : saved.result !== undefined ? prettifyJson(saved.result) : model.output
  const toolArguments = pickObject(raw, 'toolArguments')
  const restored = { ...model, output, input: { ...model.input, ...toolArguments } }
  if (name === 'Task')
    return cursorAgentPresentation(tool, restored, native, output)
  if (name.startsWith('mcp_')) {
    const source = {
      server: pickString(model.input, 'providerIdentifier'),
      tool: pickString(model.input, 'toolName') || name,
      argsJson: prettifyArgsJson(raw.toolArguments ?? model.input.args),
      content: cursorMcpContent(saved, success, output),
      status: tool.status === 'failed' || tool.status === 'cancelled' ? 'failed' as const : tool.status === 'completed' ? 'completed' as const : 'inProgress' as const,
    }
    return { ...restored, input: toolArguments ?? pickObject(model.input, 'args') ?? {}, kind: 'other', title: mcpToolCallDisplayName(source), label: 'MCP Tool Call', body: { type: 'mcp', source } }
  }
  if (tool.status !== 'completed' || native?.isError === true || !success)
    return restored

  if (name === 'Read' && typeof success.content === 'string') {
    const path = pickString(success, 'path') || pickString(restored.input, 'path')
    const range = pickObject(success, 'readRange')
    const startLine = pickNumber(range, 'startLine', undefined)
    return {
      ...restored,
      kind: 'read',
      input: { ...restored.input, path },
      output: success.content,
      body: { type: 'read', source: readFileSourceFromContent({ filePath: path, content: success.content, startLine, totalLines: pickNumber(success, 'totalLines', undefined) }) },
    }
  }
  if (name === 'StrReplace' || name === 'Write' || name === 'Delete') {
    const path = pickString(success, 'path') || pickString(restored.input, 'path')
    const patch = parseUnifiedDiffCached(pickString(success, 'diffString'))
    const source = patch
      ? fileEditDiffFromHunks(path, patch.hunks)
      : typeof success.afterFullFileContent === 'string'
        ? { filePath: path, structuredPatch: null, oldStr: pickString(success, 'beforeFullFileContent'), newStr: success.afterFullFileContent }
        : null
    if (fileEditHasDiff(source)) {
      const originalFile = pickString(success, 'beforeFullFileContent', undefined)
      return { ...restored, kind: name === 'Write' ? 'write' : 'edit', body: { type: 'diff', sources: [{ ...source, originalFile }] } }
    }
  }
  if (name === 'Grep' || name === 'Glob') {
    return {
      ...restored,
      kind: name === 'Glob' ? 'glob' : 'grep',
      label: name,
      input: { ...restored.input, pattern: pickString(restored.input, 'pattern') || pickString(restored.input, 'glob_pattern'), path: pickString(restored.input, 'path') || pickString(restored.input, 'target_directory') },
      body: { type: 'search', source: cursorSearchSource(success, restored, name === 'Glob') },
    }
  }
  if (name === 'Shell') {
    const stdout = pickString(success, 'stdout') || pickString(protocolOutput, 'stdout')
    const stderr = pickString(success, 'stderr') || pickString(protocolOutput, 'stderr')
    const text = pickString(success, 'interleavedOutput') || [stdout, stderr].filter(Boolean).join(stdout.endsWith('\n') ? '' : '\n')
    const exitCode = pickNumber(protocolOutput, 'exitCode', undefined) ?? pickNumber(success, 'exitCode', undefined)
    return {
      ...restored,
      kind: 'execute',
      title: pickString(restored.input, 'description'),
      output: text,
      body: { type: 'command', source: {
        output: text,
        stderr,
        exitCode,
        durationMs: pickNumber(success, 'localExecutionTimeMs', undefined) ?? pickNumber(success, 'executionTime', undefined),
        isError: commandIsError(pickString(tool, 'status'), exitCode),
        truncated: pickBoolean(success, 'outputTruncated') ?? false,
      } },
    }
  }
  return restored
}
