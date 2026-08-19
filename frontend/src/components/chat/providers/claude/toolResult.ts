import type { MessageCategory } from '../../messageClassification'
import type { ToolResultMeta } from '../registry'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { prettifyJson } from '~/lib/jsonFormat'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { CLAUDE_TOOL } from '~/types/toolMessages'
import { formatUnifiedDiffText } from '../../diff'
import { COLLAPSED_RESULT_ROWS, hasMoreLinesThan } from '../../results/collapse'
import { commandOutputIsCollapsible } from '../../results/commandResult'
import { fileEditDiffHunks, fileEditHasDiff } from '../../results/fileEditDiff'
import { claudeAgentFromToolResult, claudeAgentResultBody } from './extractors/agent'
import { extractToolResultText } from './extractors/assistantContent'
import { claudeFileEditFromToolUseResult } from './extractors/fileEdit'
import { claudeListAgentsListing } from './extractors/listAgents'
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
 * The body a structured card RENDERS, for the tools that have one.
 *
 * These are the only tools whose three questions below -- is it collapsible, is
 * there anything to copy, what gets copied -- have ONE answer, so reading the
 * body once here is what stops them drifting. Every other tool measures the
 * three differently (Grep counts filenames, Read reads file.numLines, Bash
 * normalizes \r-overwrites first), which is why they keep their own branches and
 * this returns null for them.
 *
 * null means "this tool has no structured body, answer from resultText". '' means
 * "it has one and it is empty" -- a launch with no prompt, which must NOT fall
 * back to resultText, because there resultText is the CLI's instructions to the
 * model and the card never shows them.
 */
function structuredResultBody(
  toolName: string,
  toolUseResult: Record<string, unknown> | undefined,
  resultText: string | null,
): string | null {
  if (toolName === CLAUDE_TOOL.LIST_AGENTS)
    return claudeListAgentsListing(toolUseResult, resultText ?? '')
  if (toolName === CLAUDE_TOOL.AGENT) {
    const source = claudeAgentFromToolResult(toolUseResult, resultText ?? '')
    return source ? claudeAgentResultBody(source) : null
  }
  return null
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

  // Judge the text the CARD shows, which for a launch is the prompt rather than
  // the harness instructions in resultText. Reading resultText there put the
  // expand button on a body the card no longer renders.
  const structuredCollapsible = structuredResultBody(toolName, toolUseResult, resultText)
  if (structuredCollapsible !== null)
    return hasMoreLinesThan(structuredCollapsible, COLLAPSED_RESULT_ROWS)

  if (toolName === CLAUDE_TOOL.WEB_FETCH && typeof toolUseResult?.code === 'number')
    return true

  if (toolName === CLAUDE_TOOL.WEB_SEARCH && Array.isArray(toolUseResult?.results))
    return true

  if (toolName === CLAUDE_TOOL.REMOTE_TRIGGER)
    return claudeRemoteTriggerFromToolResult(toolUseResult, resultText ?? '') !== null

  // Bash flows through CommandResultBody, which normalizes `\r`-overwrites
  // into separate lines. Counting raw `\n` here would hide the toolbar's
  // expand button on progress output that the body actually clips.
  if (toolName === CLAUDE_TOOL.BASH)
    return resultText != null && commandOutputIsCollapsible(resultText)

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
  // Mirrors computeCopyableContent's structured branch. Without it the default
  // answers from resultText, which extractToolResultText reports as null for an
  // empty block content -- so a card showing a structured listing, or a launch
  // showing its prompt, offered no Copy button at all.
  const structured = structuredResultBody(toolName, toolUseResult, resultText)
  if (structured !== null)
    return structured !== ''
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

  // No `|| resultText` fallback. For a launch with no prompt the body is empty
  // and resultText is the CLI's instructions to the model -- text the card
  // deliberately never shows, so Copy must not hand it over either.
  const structuredCopy = structuredResultBody(toolName, toolUseResult, resultText)
  if (structuredCopy !== null)
    return structuredCopy || null

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
