/* eslint-disable solid/components-return-once -- TOOL_RESULT_ENTRIES are dispatch functions, not Solid components; early-return is the dispatch contract (return null = fall through to MCP/catch-all) */
import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { ReadFileResultSource } from '../../../results/readFileResult'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { isObject, pickString } from '~/lib/jsonPick'
import { CLAUDE_TOOL } from '~/types/toolMessages'
import { getAssistantContent } from '../../../messageUtils'
import { pickFileEditDiff } from '../../../results/fileEditDiff'
import { McpToolCallBody } from '../../../results/mcpToolCall'
import { ReadFileResultBody } from '../../../results/readFileResult'
import { SearchResultBody } from '../../../results/searchResult'
import { WebFetchResultBody } from '../../../results/webFetchResult'
import { WebSearchResultsBody } from '../../../results/webSearchResult'
import { ToolResultMessage } from '../../../toolRenderers'
import { claudeBashFromToolResult } from '../extractors/bash'
import { claudeFileEditFromToolUseInput, claudeFileEditFromToolUseResult, isClaudeFileEditTool } from '../extractors/fileEdit'
import { claudeGlobFromToolResult, claudeGrepFromToolResult } from '../extractors/grepGlob'
import { claudeMcpFromToolResult, isClaudeMcpTool } from '../extractors/mcp'
import { claudeReadFromToolResult } from '../extractors/read'
import { claudeWebFetchFromToolResult } from '../extractors/webFetch'
import { claudeWebSearchFromToolResult } from '../extractors/webSearch'
import { AgentResultView } from './agent'
import { AskUserQuestionResultView } from './askUserQuestion'
import { ExitPlanModeResultView } from './exitPlanMode'
import { TaskOutputResultView } from './taskOutput'
import { ToolSearchResultView } from './toolSearch'

/** Extract tool name and input from a parsed tool_use message. */
function extractToolUseInfo(parsed: ParsedMessageContent): { toolName: string, input: Record<string, unknown> } | null {
  const obj = parsed.parentObject
  if (!obj)
    return null
  const content = getAssistantContent(obj)
  if (!content)
    return null
  const toolUse = content.find(c => isObject(c) && c.type === 'tool_use')
  if (!toolUse)
    return null
  const toolData = toolUse as Record<string, unknown>
  return {
    toolName: String(toolData.name || ''),
    input: isObject(toolData.input) ? toolData.input as Record<string, unknown> : {},
  }
}

/** Set of tool names whose results should be rendered as preformatted text. */
const PRE_TEXT_TOOLS: ReadonlySet<string> = new Set([
  CLAUDE_TOOL.BASH,
  CLAUDE_TOOL.GREP,
  CLAUDE_TOOL.GLOB,
  CLAUDE_TOOL.READ,
  CLAUDE_TOOL.TASK_OUTPUT,
])

type DisplayKind = 'bash' | 'read' | 'pre' | 'markdown'

/** Map a tool name and its pre-text classification to the catch-all `displayKind`. */
function pickDisplayKind(toolName: string, isPreText: boolean): DisplayKind {
  if (toolName === CLAUDE_TOOL.BASH || toolName === CLAUDE_TOOL.TASK_OUTPUT || toolName === '')
    return 'bash'
  if (toolName === CLAUDE_TOOL.READ)
    return 'read'
  return isPreText ? 'pre' : 'markdown'
}

/**
 * Inputs available to every per-tool result entry. Bundles the parsed wire
 * shape the extractors already need so individual entries don't re-derive
 * common fields.
 */
interface DispatchInfo {
  toolUseResult: Record<string, unknown> | undefined
  resultContent: string
  toolInput: Record<string, unknown> | undefined
  resultData: Record<string, unknown>
  readSource: ReadFileResultSource | null
}

type ToolResultEntry = (info: DispatchInfo, context: RenderContext | undefined) => JSX.Element | null

/**
 * Per-tool dispatch table. Each entry returns `null` to fall through to the
 * MCP/catch-all branch when its specific guard isn't met (e.g. WebFetch with
 * no numeric `code`, TaskOutput without a structured `task`). The catch-all
 * branch is the dispatcher's else-after-lookup, not an entry here.
 */
const TOOL_RESULT_ENTRIES: Record<string, ToolResultEntry> = {
  [CLAUDE_TOOL.GREP]: (info, ctx) => (
    <SearchResultBody source={claudeGrepFromToolResult(info.toolUseResult, info.resultContent)} context={ctx} />
  ),

  [CLAUDE_TOOL.GLOB]: (info, ctx) => (
    <SearchResultBody source={claudeGlobFromToolResult(info.toolUseResult, info.resultContent)} context={ctx} />
  ),

  [CLAUDE_TOOL.TOOL_SEARCH]: (info) => {
    if (!info.toolUseResult)
      return null
    const matches = Array.isArray(info.toolUseResult.matches) ? info.toolUseResult.matches as string[] : []
    return <ToolSearchResultView matches={matches} />
  },

  [CLAUDE_TOOL.READ]: (info, ctx) => {
    if (!info.readSource || info.readSource.lines === null)
      return null
    return <ReadFileResultBody source={info.readSource} context={ctx} />
  },

  [CLAUDE_TOOL.AGENT]: (info, ctx) => {
    if (!info.toolUseResult)
      return null
    const agentId = pickString(info.toolUseResult, 'agentId')
    const status = pickString(info.toolUseResult, 'status', 'completed')
    const agentContent = Array.isArray(info.toolUseResult.content)
      ? (info.toolUseResult.content as Array<Record<string, unknown>>)
          .filter(c => isObject(c) && c.type === 'text')
          .map(c => String(c.text || ''))
          .join('\n\n')
      : info.resultContent
    return <AgentResultView agentId={agentId} status={status} content={agentContent} context={ctx} />
  },

  [CLAUDE_TOOL.TASK_OUTPUT]: (info, ctx) => {
    if (!info.toolUseResult || !isObject(info.toolUseResult.task))
      return null
    return (
      <TaskOutputResultView
        task={info.toolUseResult.task as Record<string, unknown>}
        fallbackContent={info.resultContent}
        context={ctx}
      />
    )
  },

  [CLAUDE_TOOL.ASK_USER_QUESTION]: (info, ctx) => {
    if (!info.toolUseResult)
      return null
    return <AskUserQuestionResultView toolUseResult={info.toolUseResult} context={ctx} />
  },

  [CLAUDE_TOOL.WEB_FETCH]: (info, ctx) => {
    if (!info.toolUseResult || typeof info.toolUseResult.code !== 'number')
      return null
    return <WebFetchResultBody source={claudeWebFetchFromToolResult(info.toolUseResult, info.resultContent)!} context={ctx} />
  },

  [CLAUDE_TOOL.WEB_SEARCH]: (info, ctx) => {
    if (!info.toolUseResult || !Array.isArray(info.toolUseResult.results))
      return null
    return <WebSearchResultsBody source={claudeWebSearchFromToolResult(info.toolUseResult)!} context={ctx} />
  },

  [CLAUDE_TOOL.EXIT_PLAN_MODE]: (info, ctx) => (
    <ExitPlanModeResultView
      isError={info.resultData.is_error === true}
      resultContent={info.resultContent}
      toolUseResult={info.toolUseResult}
      context={ctx}
    />
  ),
}

/** Render a Claude tool_result message. */
export function renderClaudeToolResult(
  parsed: unknown,
  context?: RenderContext,
): JSX.Element | null {
  if (!isObject(parsed) || parsed.type !== 'user')
    return null

  const message = parsed.message as Record<string, unknown>
  if (!isObject(message))
    return null

  const content = message.content
  if (!Array.isArray(content))
    return null

  // Find tool_result in content array
  const toolResult = content.find((c: unknown) =>
    isObject(c) && c.type === 'tool_result',
  )
  if (!toolResult)
    return null

  const resultData = toolResult as Record<string, unknown>
  const resultContent = Array.isArray(resultData.content)
    ? (resultData.content as Array<Record<string, unknown>>)
        .filter(c => isObject(c) && c.type === 'text')
        .map(c => c.text)
        .join('')
    : String(resultData.content || '')

  // Extract tool name: prefer span_type (always set for span messages),
  // then tool_use_result, then linked tool_use message.
  const toolUseResult = parsed.tool_use_result as Record<string, unknown> | undefined
  const toolUseInfo = context?.toolUseParsed ? extractToolUseInfo(context.toolUseParsed) : null
  const toolName = String(context?.spanType || toolUseResult?.tool_name || toolUseInfo?.toolName || context?.parentToolName || '')
  const toolInput = toolUseInfo?.input

  // Build a Read result source via the shared extractor; null for non-text
  // Read variants (image/notebook/pdf/parts/file_unchanged), which fall
  // through to the catch-all renderer's existing handling.
  const readSource = toolName === CLAUDE_TOOL.READ
    ? claudeReadFromToolResult({ toolUseResult, resultContent, toolInput })
    : null

  // Try the per-tool entry; null means "fall through to MCP/catch-all".
  const entry = TOOL_RESULT_ENTRIES[toolName]
  const dispatchInfo: DispatchInfo = { toolUseResult, resultContent, toolInput, resultData, readSource }
  if (entry) {
    const result = entry(dispatchInfo, context)
    if (result !== null)
      return result
  }

  // MCP (mcp__server__tool): render args + content blocks via the shared body.
  if (isClaudeMcpTool(toolName)) {
    const mcpSource = claudeMcpFromToolResult({
      toolName,
      toolInput,
      toolUseResult,
      resultContent: Array.isArray(resultData.content) ? resultData.content : resultContent,
      isError: resultData.is_error === true,
    })
    if (mcpSource)
      return <McpToolCallBody source={mcpSource} context={context} />
  }

  // Catch-all: shared `ToolResultMessage`. Reads `displayKind` /
  // `effectiveDiff` / `readFilePath` / `hideContent` — computed only here
  // since none of the per-tool entries above need them.
  const isPreText = toolName === '' || PRE_TEXT_TOOLS.has(toolName)
  const displayKind = pickDisplayKind(toolName, isPreText)
  const effectiveDiff = isClaudeFileEditTool(toolName)
    ? pickFileEditDiff(
        claudeFileEditFromToolUseResult(toolUseResult),
        toolUseInfo ? claudeFileEditFromToolUseInput(toolUseInfo.toolName, toolUseInfo.input) : null,
      )
    : null
  const readFilePath = toolName === CLAUDE_TOOL.READ
    ? (readSource?.filePath || String(toolInput?.file_path || ''))
    : undefined
  const hideContent = effectiveDiff !== null
  const isErrorVal = typeof resultData.is_error === 'boolean' ? resultData.is_error : undefined
  const commandResult = toolName === CLAUDE_TOOL.BASH
    ? claudeBashFromToolResult({ toolUseResult, resultContent, isError: isErrorVal })
    : null

  return (
    <ToolResultMessage
      resultContent={hideContent ? '' : resultContent}
      displayKind={displayKind}
      diffSource={effectiveDiff}
      readFilePath={readFilePath}
      isError={isErrorVal}
      commandResult={commandResult}
      context={context}
    />
  )
}
