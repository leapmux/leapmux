import type { MessageCategory } from '../../messageClassification'
import type { ToolResultMeta } from '../registry'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { isObject, pickString } from '~/lib/jsonPick'
import { CLAUDE_TOOL } from '~/types/toolMessages'
import { formatUnifiedDiffText } from '../../diff'
import { fileEditDiffHunks, fileEditHasDiff } from '../../results/fileEditDiff'
import { COLLAPSED_RESULT_ROWS } from '../../toolRenderers'
import { claudeFileEditFromToolUseResult } from './extractors/fileEdit'

/** Pull the text content out of a Claude tool_result block inside a parsed message object. */
function extractToolResultText(parsedObj: Record<string, unknown> | null | undefined): string | null {
  if (!parsedObj)
    return null
  const msg = parsedObj.message as Record<string, unknown> | undefined
  if (!msg || !Array.isArray(msg.content))
    return null
  const tr = (msg.content as Array<Record<string, unknown>>).find(c => isObject(c) && c.type === 'tool_result')
  if (!tr)
    return null
  const text = Array.isArray(tr.content)
    ? (tr.content as Array<Record<string, unknown>>).filter(c => isObject(c) && c.type === 'text').map(c => c.text).join('')
    : String(tr.content || '')
  return text || null
}

/** Resolve toolName + tool_use_result for a Claude tool_result message. */
function extractToolResultInfo(
  parsedObj: Record<string, unknown> | null | undefined,
  spanType: string | undefined,
): { toolName: string, toolUseResult: Record<string, unknown> | undefined } | null {
  if (!parsedObj)
    return null
  const toolUseResult = parsedObj.tool_use_result as Record<string, unknown> | undefined
  const toolName = String(spanType || pickString(toolUseResult, 'tool_name') || '')
  if (!toolName && !toolUseResult)
    return null
  return { toolName, toolUseResult }
}

/** Per-tool collapsibility check for Claude tool_result messages. */
function isCollapsible(
  toolName: string,
  toolUseResult: Record<string, unknown> | undefined,
  parsedObj: Record<string, unknown>,
): boolean {
  if (toolName === CLAUDE_TOOL.GREP || toolName === CLAUDE_TOOL.GLOB) {
    const filenames = Array.isArray(toolUseResult?.filenames) ? toolUseResult.filenames as string[] : []
    if (filenames.length > COLLAPSED_RESULT_ROWS)
      return true
    if (toolName === CLAUDE_TOOL.GREP && typeof toolUseResult?.content === 'string'
      && (toolUseResult.content as string).split('\n').length > COLLAPSED_RESULT_ROWS) {
      return true
    }
    const rc = extractToolResultText(parsedObj)
    return rc != null && rc.split('\n').filter((l: string) => l.trim()).length > COLLAPSED_RESULT_ROWS
  }

  if (toolName === CLAUDE_TOOL.READ) {
    const file = toolUseResult?.file as Record<string, unknown> | undefined
    if (file && typeof file.numLines === 'number')
      return (file.numLines as number) > COLLAPSED_RESULT_ROWS
  }

  if (toolName === CLAUDE_TOOL.AGENT) {
    if (Array.isArray(toolUseResult?.content)
      && (toolUseResult.content as Array<Record<string, unknown>>).some(c => isObject(c) && c.type === 'text')) {
      return true
    }
    const rc = extractToolResultText(parsedObj)
    return rc != null && rc.split('\n').length > COLLAPSED_RESULT_ROWS
  }

  if (toolName === CLAUDE_TOOL.WEB_FETCH && typeof toolUseResult?.code === 'number')
    return true

  if (toolName === CLAUDE_TOOL.WEB_SEARCH && Array.isArray(toolUseResult?.results))
    return true

  const rc = extractToolResultText(parsedObj)
  return rc != null && rc.split('\n').length > COLLAPSED_RESULT_ROWS
}

/** Compute copyable text for a Claude tool_result. Heavy formatting (Edit's unified diff) only runs when invoked. */
function computeCopyableContent(
  toolName: string,
  toolUseResult: Record<string, unknown> | undefined,
  parsedObj: Record<string, unknown>,
): string | null {
  if (toolName === CLAUDE_TOOL.EDIT) {
    const src = claudeFileEditFromToolUseResult(toolUseResult)
    if (!fileEditHasDiff(src))
      return null
    return formatUnifiedDiffText(fileEditDiffHunks(src), src.filePath)
  }

  if (toolName === CLAUDE_TOOL.READ) {
    const file = toolUseResult?.file as Record<string, unknown> | undefined
    if (file && typeof file.content === 'string')
      return file.content as string
  }

  if (toolName === CLAUDE_TOOL.WRITE && typeof toolUseResult?.newString === 'string')
    return toolUseResult.newString as string

  return extractToolResultText(parsedObj)
}

/**
 * ProviderPlugin.toolResultMeta implementation for Claude Code.
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

  return {
    collapsible: isCollapsible(toolName, toolUseResult, obj),
    hasDiff: fileEditHasDiff(claudeFileEditFromToolUseResult(toolUseResult)),
    copyableContent: () => computeCopyableContent(toolName, toolUseResult, obj),
  }
}
