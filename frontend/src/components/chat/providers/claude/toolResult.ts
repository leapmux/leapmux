import type { MessageCategory } from '../../messageClassification'
import type { ToolResultMeta } from '../registry'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { prettifyJson } from '~/lib/jsonFormat'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { CLAUDE_TOOL } from '~/types/toolMessages'
import { formatUnifiedDiffText } from '../../diff'
import { fileEditDiffHunks, fileEditHasDiff } from '../../results/fileEditDiff'
import { hasMoreLinesThan } from '../../results/useCollapsedLines'
import { COLLAPSED_RESULT_ROWS } from '../../toolRenderers'
import { extractToolResultText } from './extractors/assistantContent'
import { claudeFileEditFromToolUseResult } from './extractors/fileEdit'
import { claudeRemoteTriggerFromToolResult } from './extractors/remoteTrigger'

/** Resolve toolName + tool_use_result for a Claude tool_result message. */
function extractToolResultInfo(
  parsedObj: Record<string, unknown> | null | undefined,
  spanType: string | undefined,
): { toolName: string, toolUseResult: Record<string, unknown> | undefined } | null {
  if (!parsedObj)
    return null
  const toolUseResult = pickObject(parsedObj, 'tool_use_result') ?? undefined
  const toolName = String(spanType || pickString(toolUseResult, 'tool_name') || '')
  if (!toolName && !toolUseResult)
    return null
  return { toolName, toolUseResult }
}

/**
 * Per-tool collapsibility check. `resultText` is the (possibly null) cached
 * tool_result text for the current message; pass it in so `claudeToolResultMeta`
 * only walks the content array once.
 */
function isCollapsible(
  toolName: string,
  toolUseResult: Record<string, unknown> | undefined,
  resultText: string | null,
): boolean {
  if (toolName === CLAUDE_TOOL.GREP || toolName === CLAUDE_TOOL.GLOB) {
    const filenames = Array.isArray(toolUseResult?.filenames) ? toolUseResult.filenames as string[] : []
    if (filenames.length > COLLAPSED_RESULT_ROWS)
      return true
    if (toolName === CLAUDE_TOOL.GREP && typeof toolUseResult?.content === 'string'
      && hasMoreLinesThan(toolUseResult.content as string, COLLAPSED_RESULT_ROWS)) {
      return true
    }
    return resultText != null && resultText.split('\n').filter((l: string) => l.trim()).length > COLLAPSED_RESULT_ROWS
  }

  if (toolName === CLAUDE_TOOL.READ) {
    const file = pickObject(toolUseResult, 'file')
    if (file && typeof file.numLines === 'number')
      return (file.numLines as number) > COLLAPSED_RESULT_ROWS
  }

  if (toolName === CLAUDE_TOOL.AGENT) {
    if (Array.isArray(toolUseResult?.content)
      && (toolUseResult.content as Array<Record<string, unknown>>).some(c => isObject(c) && c.type === 'text')) {
      return true
    }
    return resultText != null && hasMoreLinesThan(resultText, COLLAPSED_RESULT_ROWS)
  }

  if (toolName === CLAUDE_TOOL.WEB_FETCH && typeof toolUseResult?.code === 'number')
    return true

  if (toolName === CLAUDE_TOOL.WEB_SEARCH && Array.isArray(toolUseResult?.results))
    return true

  if (toolName === CLAUDE_TOOL.REMOTE_TRIGGER)
    return claudeRemoteTriggerFromToolResult(toolUseResult, resultText ?? '') !== null

  return resultText != null && hasMoreLinesThan(resultText, COLLAPSED_RESULT_ROWS)
}

/**
 * Cheap presence-check that mirrors `computeCopyableContent`'s null branches.
 * Used by the toolbar to decide whether to show the Copy button without paying
 * the formatting cost of `computeCopyableContent` on every render.
 */
function hasCopyable(
  toolName: string,
  toolUseResult: Record<string, unknown> | undefined,
  hasEditDiff: boolean,
  resultText: string | null,
): boolean {
  if (toolName === CLAUDE_TOOL.EDIT)
    return hasEditDiff
  if (toolName === CLAUDE_TOOL.READ) {
    const file = pickObject(toolUseResult, 'file')
    return !!file && typeof file.content === 'string'
  }
  if (toolName === CLAUDE_TOOL.WRITE)
    return typeof toolUseResult?.newString === 'string'
  return resultText !== null
}

/** Compute copyable text for a Claude tool_result. Heavy formatting (Edit's unified diff) only runs when invoked. */
function computeCopyableContent(
  toolName: string,
  toolUseResult: Record<string, unknown> | undefined,
  resultText: string | null,
): string | null {
  if (toolName === CLAUDE_TOOL.EDIT) {
    const src = claudeFileEditFromToolUseResult(toolUseResult)
    if (!fileEditHasDiff(src))
      return null
    return formatUnifiedDiffText(fileEditDiffHunks(src), src.filePath)
  }

  if (toolName === CLAUDE_TOOL.READ) {
    const file = pickObject(toolUseResult, 'file')
    if (file && typeof file.content === 'string')
      return file.content as string
  }

  if (toolName === CLAUDE_TOOL.WRITE && typeof toolUseResult?.newString === 'string')
    return toolUseResult.newString as string

  if (toolName === CLAUDE_TOOL.REMOTE_TRIGGER) {
    const source = claudeRemoteTriggerFromToolResult(toolUseResult, resultText ?? '')
    if (source)
      return `HTTP ${source.status}\n${prettifyJson(source.parsed ?? source.json)}`
  }

  return resultText
}

/**
 * Provider.toolResultMeta implementation for Claude Code.
 *
 * Returns null for any non-tool_result category. The `toolUseParsed` argument
 * is currently unused (Claude reads everything off the result message + its
 * `tool_use_result` payload), but kept to keep the plugin signature uniform.
 */
export function claudeToolResultMeta(
  category: MessageCategory,
  parsed: unknown,
  spanType: string | undefined,
  _toolUseParsed: ParsedMessageContent | undefined,
): ToolResultMeta | null {
  if (category.kind !== 'tool_result')
    return null

  const obj = isObject(parsed) ? parsed as Record<string, unknown> : null
  if (!obj)
    return null

  const info = extractToolResultInfo(obj, spanType)
  if (!info)
    return null

  const { toolName, toolUseResult } = info
  // Walk message.content once; downstream collapsibility/copy paths reuse it.
  const resultText = extractToolResultText(obj)
  const hasEditDiff = fileEditHasDiff(claudeFileEditFromToolUseResult(toolUseResult))

  return {
    collapsible: isCollapsible(toolName, toolUseResult, resultText),
    hasDiff: hasEditDiff,
    hasCopyable: hasCopyable(toolName, toolUseResult, hasEditDiff, resultText),
    copyableContent: () => computeCopyableContent(toolName, toolUseResult, resultText),
  }
}
