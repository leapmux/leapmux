/* eslint-disable solid/components-return-once -- TOOL_RESULT_ENTRIES are dispatch functions, not Solid components; early-return is the dispatch contract (return null = fall through to MCP/catch-all) */
import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { ReadFileResultSource } from '../../../results/readFileResult'
import { joinContentParagraphs } from '~/lib/contentBlocks'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { CLAUDE_TOOL } from '~/types/toolMessages'
import { cachedRenderValue } from '../../../messageRenderCache'
import { pickFileEditDiff } from '../../../results/fileEditDiff'
import { McpToolCallBody } from '../../../results/mcpToolCall'
import { ReadFileResultBody } from '../../../results/readFileResult'
import { SearchResultBody } from '../../../results/searchResult'
import { WebFetchResultBody } from '../../../results/webFetchResult'
import { WebSearchResultsBody } from '../../../results/webSearchResults'
import { ToolResultMessage } from '../../../toolRenderers'
import { extractToolUseInfo, getMessageContentArray } from '../extractors/assistantContent'
import { claudeBashFromToolResult } from '../extractors/bash'
import { claudeCreateResultDiff, claudeFileEditFromToolUseInput, claudeFileEditFromToolUseResult, isClaudeFileEditTool } from '../extractors/fileEdit'
import { claudeGlobFromToolResult, claudeGrepFromToolResult } from '../extractors/grepGlob'
import { claudeMcpFromToolResult, isClaudeMcpTool } from '../extractors/mcp'
import { claudeReadFromToolResult } from '../extractors/read'
import { claudeRemoteTriggerFromToolResult } from '../extractors/remoteTrigger'
import { claudeWebFetchFromToolResult } from '../extractors/webFetch'
import { claudeWebSearchFromToolResult } from '../extractors/webSearch'
import { AgentResultView } from './agent'
import { AskUserQuestionResultView } from './askUserQuestion'
import { ExitPlanModeResultView } from './exitPlanMode'
import { RemoteTriggerResultView } from './remoteTrigger'
import { TaskOutputResultView } from './taskOutput'
import { ToolSearchResultView } from './toolSearch'

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

interface DispatchState extends DispatchInfo {
  toolName: string
  toolUseInfo: ReturnType<typeof extractToolUseInfo> | null
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
      ? joinContentParagraphs(info.toolUseResult.content as Array<Record<string, unknown>>, { text: 'text' })
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
    const source = claudeWebFetchFromToolResult(info.toolUseResult, info.resultContent)
    if (!source)
      return null
    return <WebFetchResultBody source={source} context={ctx} />
  },

  [CLAUDE_TOOL.WEB_SEARCH]: (info, ctx) => {
    const source = claudeWebSearchFromToolResult(info.toolUseResult)
    if (!source)
      return null
    return <WebSearchResultsBody source={source} context={ctx} />
  },

  [CLAUDE_TOOL.EXIT_PLAN_MODE]: (info, ctx) => (
    <ExitPlanModeResultView
      isError={info.resultData.is_error === true}
      resultContent={info.resultContent}
      toolUseResult={info.toolUseResult}
      context={ctx}
    />
  ),

  [CLAUDE_TOOL.REMOTE_TRIGGER]: (info, ctx) => {
    const source = claudeRemoteTriggerFromToolResult(info.toolUseResult, info.resultContent)
    if (!source)
      return null
    return <RemoteTriggerResultView source={source} context={ctx} />
  },

  // Claude Task* tool_result messages are classified `'hidden'` in
  // plugin.tsx, so they never reach this dispatch. The tool_use side
  // (toolUse/taskTools.tsx) renders the single-row cards (TaskCreate,
  // TaskUpdate, TaskGet) by reading the paired result through
  // `context.toolResultParsed`. TaskList is hidden on both sides
  // because the persistent todo sidebar already surfaces the list.
}

/** Render a Claude tool_result message. */
export function renderClaudeToolResult(
  parsed: unknown,
  context?: RenderContext,
): JSX.Element | null {
  if (!isObject(parsed) || parsed.type !== 'user')
    return null

  const content = getMessageContentArray(parsed)
  if (!content)
    return null

  // Find tool_result in content array
  const toolResult = content.find((c: unknown) =>
    isObject(c) && c.type === 'tool_result',
  )
  if (!toolResult)
    return null

  const dispatch = cachedRenderValue(context, 'claude.toolResult.dispatchState', (): DispatchState => {
    const resultData = toolResult as Record<string, unknown>
    const resultContent = Array.isArray(resultData.content)
      ? joinContentParagraphs(resultData.content as Array<Record<string, unknown>>, { text: 'text' })
      : String(resultData.content || '')

    // Extract tool name: prefer span_type (always set for span messages),
    // then tool_use_result, then linked tool_use message.
    const toolUseResult = pickObject(parsed, 'tool_use_result') ?? undefined
    const toolUseInfo = context?.toolUseParsed ? extractToolUseInfo(context.toolUseParsed) : null
    const toolName = String(context?.spanType || toolUseResult?.tool_name || toolUseInfo?.toolName || '')
    const toolInput = toolUseInfo?.input

    // Build a Read result source via the shared extractor; null for non-text
    // Read variants (image/notebook/pdf/parts/file_unchanged), which fall
    // through to the catch-all renderer's existing handling.
    const readSource = toolName === CLAUDE_TOOL.READ
      ? claudeReadFromToolResult({ toolUseResult, resultContent, toolInput })
      : null
    return { toolName, toolUseInfo, toolUseResult, resultContent, toolInput, resultData, readSource }
  })

  // Try the per-tool entry; null means "fall through to MCP/catch-all".
  const entry = TOOL_RESULT_ENTRIES[dispatch.toolName]
  const dispatchInfo: DispatchInfo = dispatch
  if (entry) {
    const result = entry(dispatchInfo, context)
    if (result !== null)
      return result
  }

  // MCP (mcp__server__tool): render args + content blocks via the shared body.
  if (isClaudeMcpTool(dispatch.toolName)) {
    const mcpSource = claudeMcpFromToolResult({
      toolName: dispatch.toolName,
      toolInput: dispatch.toolInput,
      toolUseResult: dispatch.toolUseResult,
      resultContent: Array.isArray(dispatch.resultData.content) ? dispatch.resultData.content : dispatch.resultContent,
      isError: dispatch.resultData.is_error === true,
    })
    if (mcpSource)
      return <McpToolCallBody source={mcpSource} context={context} />
  }

  // Catch-all: shared `ToolResultMessage`. Reads `displayKind` /
  // `effectiveDiff` / `readFilePath` — computed only here since none of the
  // per-tool entries above need them.
  const isPreText = dispatch.toolName === '' || PRE_TEXT_TOOLS.has(dispatch.toolName)
  const displayKind = pickDisplayKind(dispatch.toolName, isPreText)
  const isErrorVal = typeof dispatch.resultData.is_error === 'boolean' ? dispatch.resultData.is_error : undefined
  // When the tool failed (`is_error: true`), the edit was *not* applied — fall
  // back to text rendering so the error message surfaces instead of a diff
  // synthesized from the tool_use input.
  // A Write/create whose tool_use sibling is absent carries the whole new file in
  // the result's `content`; claudeCreateResultDiff recovers it as an all-added diff
  // (shared with the extractor so all render paths agree) instead of
  // dropping to a one-line "File created successfully".
  const effectiveDiff = (isClaudeFileEditTool(dispatch.toolName) && isErrorVal !== true
    ? pickFileEditDiff(
        claudeFileEditFromToolUseResult(dispatch.toolUseResult),
        dispatch.toolUseInfo ? claudeFileEditFromToolUseInput(dispatch.toolUseInfo.toolName, dispatch.toolUseInfo.input) : null,
      )
    : null) ?? claudeCreateResultDiff(dispatch.toolUseResult, isErrorVal === true)
  const readFilePath = dispatch.toolName === CLAUDE_TOOL.READ
    ? (dispatch.readSource?.filePath || String(dispatch.toolInput?.file_path || ''))
    : undefined
  const commandResult = dispatch.toolName === CLAUDE_TOOL.BASH
    ? claudeBashFromToolResult({ toolUseResult: dispatch.toolUseResult, resultContent: dispatch.resultContent, isError: isErrorVal })
    : null

  return (
    <ToolResultMessage
      resultContent={effectiveDiff !== null ? '' : dispatch.resultContent}
      displayKind={displayKind}
      diffSource={effectiveDiff}
      readFilePath={readFilePath}
      isError={isErrorVal}
      commandResult={commandResult}
      context={context}
    />
  )
}
