import type { AgentResultSource } from '../../../results/agentResult'
import type { CommandResultSource } from '../../../results/commandResult'
import type { FileEditDiffSource } from '../../../results/fileEditDiff'
import type { ReadFileResultSource } from '../../../results/readFileResult'
import type { SearchResultSource } from '../../../results/searchResult'
import type { WebFetchResultSource } from '../../../results/webFetchResult'
import type { TodoListSource } from '../../../todoListMessage'
import type { ToolResultMeta } from '../../registry'
import type { ZCodeResultDisplay } from './display'
import type { ZCodeRow } from './toolCommon'
import { ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { pickString } from '~/lib/jsonPick'
import { todosToMarkdown } from '~/lib/messageParser'
import { COLLAPSED_RESULT_ROWS, hasMoreLinesThan } from '../../../results/collapse'
import { commandOutputIsCollapsible } from '../../../results/commandResult'
import { fileEditCopyableText, fileEditHasDiff } from '../../../results/fileEditDiff'
import { mcpToolResultMeta } from '../../../results/mcpToolCall'
import { searchResultCollapsible } from '../../../results/searchResult'
import { ZCODE_WEB_FETCH } from '../protocol'
import { zcodeAgentResult } from './agent'
import { extractZCodeBash, zcodeBashToCommandSource } from './bash'
import { zcodeResultDisplay } from './display'
import { extractZCodeFileDiff, extractZCodeRead } from './fileEdit'
import { extractZCodeSearch } from './search'
import { zcodeErrorText, zcodeExtractTool, zcodeTodoListFromInput, zcodeToolInput } from './toolCommon'

export type ZCodeResultBody
  = | { kind: 'display', source: ZCodeResultDisplay }
    | { kind: 'agent', source: AgentResultSource }
    | { kind: 'command', source: CommandResultSource }
    | { kind: 'diff', source: FileEditDiffSource }
    | { kind: 'read', source: ReadFileResultSource }
    | { kind: 'search', source: SearchResultSource }
    | { kind: 'fetch', source: WebFetchResultSource }
    | { kind: 'todo', source: TodoListSource }
    | { kind: 'text', text: string }

/** Body and toolbar consumers use the same extraction rules. */
export interface ZCodeResultPresentation {
  body: ZCodeResultBody
  failed: boolean
  truncated: boolean
}

export function zcodeResultPresentation(row: ZCodeRow): ZCodeResultPresentation | null {
  const update = zcodeExtractTool(row.parsed)
  if (!update)
    return null
  const text = update.isError ? zcodeErrorText(update) || 'Tool call failed' : update.result?.content ?? ''
  const display = zcodeResultDisplay(row)
  const presentation: ZCodeResultPresentation = { body: { kind: 'text', text }, failed: update.isError, truncated: update.result?.display?.truncated === true || update.result?.truncated === true }
  if (display && (display.kind === 'mcp' || display.kind === 'status' || !update.isError)) {
    presentation.body = { kind: 'display', source: display }
    return presentation
  }
  const diff = extractZCodeFileDiff(row)
  if (diff) {
    presentation.body = { kind: 'diff', source: diff }
    return presentation
  }
  const command = extractZCodeBash(row)
  if (command) {
    presentation.body = { kind: 'command', source: zcodeBashToCommandSource(command) }
    return presentation
  }
  if (row.toolName === ZCODE_TOOL.Agent) {
    const source = zcodeAgentResult(row)
    if (source) {
      presentation.body = { kind: 'agent', source }
      return presentation
    }
  }
  if (update.isError)
    return presentation
  if (row.toolName === ZCODE_TOOL.TodoWrite) {
    const source = zcodeTodoListFromInput(zcodeToolInput(row))
    if (source) {
      presentation.body = { kind: 'todo', source }
      return presentation
    }
  }
  const read = extractZCodeRead(row)
  if (read) {
    presentation.body = { kind: 'read', source: read.source }
    return presentation
  }
  const search = extractZCodeSearch(row)
  if (search) {
    presentation.body = { kind: 'search', source: search }
    return presentation
  }
  if (row.toolName === ZCODE_WEB_FETCH) {
    presentation.body = { kind: 'fetch', source: { result: text, url: pickString(zcodeToolInput(row), 'url'), durationMs: update.durationMs ?? undefined } }
  }
  return presentation
}

export function zcodeResultMeta(presentation: ZCodeResultPresentation): ToolResultMeta {
  const body = presentation.body
  if (body.kind === 'display' && body.source.kind === 'mcp')
    return mcpToolResultMeta(body.source.source)
  if (body.kind === 'diff') {
    const text = fileEditCopyableText(body.source)
    return { hasDiff: fileEditHasDiff(body.source), collapsible: false, hasCopyable: !!text, copyableContent: () => text || null }
  }
  let text: string
  let collapsible: boolean
  switch (body.kind) {
    case 'agent':
      text = body.source.body
      collapsible = hasMoreLinesThan(text, COLLAPSED_RESULT_ROWS)
      break
    case 'todo':
      text = todosToMarkdown(body.source.todos)
      collapsible = false
      break
    case 'command':
      text = body.source.output
      collapsible = commandOutputIsCollapsible(text)
      break
    case 'read':
      text = body.source.lines?.map(line => line.text).join('\n') ?? body.source.fallbackContent
      collapsible = (body.source.lines?.length ?? 0) > COLLAPSED_RESULT_ROWS
      break
    case 'search':
      text = body.source.fallbackContent
      collapsible = searchResultCollapsible(body.source)
      break
    case 'fetch':
      text = body.source.result
      collapsible = hasMoreLinesThan(text, COLLAPSED_RESULT_ROWS)
      break
    case 'display':
      text = body.source.kind === 'mcp' ? '' : body.source.output
      collapsible = hasMoreLinesThan(text, COLLAPSED_RESULT_ROWS)
      break
    case 'text':
      text = body.text
      collapsible = hasMoreLinesThan(text, COLLAPSED_RESULT_ROWS)
  }
  return { hasDiff: false, collapsible, hasCopyable: !!text, copyableContent: () => text || null }
}
