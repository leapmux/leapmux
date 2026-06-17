import type { HeightInput, RowUiState } from '../../chatHeightEstimator'
import type { MessageCategory } from '../../messageClassification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { pickString, stringArray } from '~/lib/jsonPick'
import { normalizedCommandBody } from '~/lib/normalizeProgressOutput'
import { CODEX_ITEM, CODEX_STATUS } from '~/types/toolMessages'
import { countLines, markdownBody } from '../../chatHeightShared'
import { diffFieldsFromSources, fileEditDiffFromHunks, fileEditDiffFromNewFile } from '../../results/fileEditDiff'
import { commandResultBodyFields } from '../commandResultHeight'
import { codexCommandFromItem, stripToolUseHeaderFromOutput } from './extractors/commandExecution'
import { buildFileChangeShape, completedFileChangeEntries } from './extractors/fileChange'
import { codexMcpFromItem } from './extractors/mcp'
import { extractItem } from './renderHelpers'
import { isCodexTerminalStatus } from './status'

/**
 * A settled Codex `commandExecution` renders ToolResultMessage/CommandResultBody (no
 * tool-title header): a collapsible mono command-output body plus a status header only
 * on a non-Success result. Mirrors the renderer's normalize+strip transform and its
 * carriage-return-widened collapse threshold so the off-screen estimate matches what
 * mounts. Null while in progress (streaming, measured at the tail).
 */
function codexCommandFields(item: Record<string, unknown>, state: RowUiState): Partial<HeightInput> | null {
  if (!isCodexTerminalStatus(pickString(item, 'status')))
    return null
  const source = codexCommandFromItem(item)
  if (!source)
    return null
  // Codex prefixes its command output with a tool-use header line the renderer strips,
  // so normalize the STRIPPED output; the shared helper sizes the rest of the body.
  const body = normalizedCommandBody(stripToolUseHeaderFromOutput(source.output))
  return commandResultBodyFields(source, body, state.collapsed)
}

/**
 * A settled (terminal) Codex MCP / dynamic tool call renders an `alwaysVisible`
 * ToolUseLayout body (McpToolCallBody): an optional "Arguments" mono block, the
 * markdown content blocks, an optional "Structured" mono block, and an error line.
 * Size the full body (uncollapsed) + the args/structured line counts + any images,
 * mirroring claudeMcpHeightFields. Null while in progress (collapsed/streaming).
 */
function codexMcpFields(item: Record<string, unknown>): Partial<HeightInput> | null {
  const source = codexMcpFromItem(item)
  if (!source || !isCodexTerminalStatus(source.status))
    return null
  const textParts = source.content.map(c => (c.type === 'text' ? c.text : '')).filter(Boolean)
  if (source.error)
    textParts.push(source.error)
  const imageCount = source.content.filter(c => c.type === 'image').length
  const fields: Partial<HeightInput> = {
    toolUseRendersResultBody: true,
    toolHeaderLine: true,
    uncollapsed: true,
    ...markdownBody(textParts.join('\n\n')),
    hasHeader: false,
  }
  if (source.argsJson)
    fields.argsLineCount = countLines(source.argsJson)
  if (source.structuredJson)
    fields.structuredLineCount = countLines(source.structuredJson)
  if (imageCount > 0)
    fields.imageCount = imageCount
  return fields
}

/**
 * A Codex `collabAgentToolCall` with a prompt (spawnAgent / in-progress wait) renders
 * a ToolUseLayout header + a collapsible markdown prompt preview (CODEX_COLLAB_AGENT_
 * TOOL_CALL key, clamped to ~3.6rem when collapsed). Size the prompt as markdown,
 * collapsed per the resolved state -- a long collapsed prompt over-estimates slightly
 * vs the pixel clamp, the safe bias-up direction. Null for wait/closeAgent/no-prompt
 * rows (header only).
 */
function codexCollabFields(item: Record<string, unknown>, state: RowUiState): Partial<HeightInput> | null {
  const prompt = pickString(item, 'prompt')
  if (!prompt)
    return null
  return {
    toolUseRendersResultBody: true,
    toolHeaderLine: true,
    ...markdownBody(prompt),
    collapsed: state.collapsed,
    hasHeader: false,
  }
}

/**
 * Codex's `Provider.heightMetrics`: diff geometry for a completed `fileChange`
 * tool_use row, the only Codex row that renders a diff. Reads the changes the
 * same way `renderCompletedFileChange` does -- a simple-add renders one all-added
 * block, a simple-delete renders a text line (no diff), and everything else maps
 * each completed change entry to its own block -- so the estimate honors the
 * per-file block geometry the row mounts with.
 *
 * Routes every block through `diffFieldsFromSources` (like Pi) rather than
 * flattening all files' hunks into one `diffHeightFields` call. The flattened
 * form would set no `diffBlockCount`, so the estimator charged container chrome
 * for a SINGLE block (under-counting an N-file edit by (N-1) chromes) and let a
 * cross-file hunk boundary spuriously trip the between-hunk separator test.
 * Command-execution, MCP, and other Codex tool rows route through the
 * orchestrator's generic text sizing.
 */
export function codexHeightMetrics(
  category: MessageCategory,
  parsed: ParsedMessageContent,
  _toolUseParsed: ParsedMessageContent | undefined,
  state: RowUiState,
): Partial<HeightInput> | null {
  // A Codex reasoning row (classified assistant_thinking) persists its text in
  // item.summary / item.content, NOT message.content, so extractText sizes an empty
  // bubble and an expanded reasoning row under-estimates by its whole body (off-screen
  // drift). Size the persisted text the renderer falls back to off-screen -- the live
  // stream is measured at the tail -- mirroring CodexReasoningRenderer's
  // `summary.join('\n') || content.join('\n')`. estimateCollapsibleProse sizes the body
  // only when the row is expanded (thinking's default), so a collapsed row stays a header.
  if (category.kind === 'assistant_thinking') {
    const item = extractItem(parsed.parentObject)
    if (!item || pickString(item, 'type') !== CODEX_ITEM.REASONING)
      return null
    const summary = stringArray(item.summary)
    const content = stringArray(item.content)
    const text = summary.length > 0 ? summary.join('\n') : content.join('\n')
    if (!text)
      return null
    return { textLength: text.length, logicalLineCount: countLines(text) }
  }
  if (category.kind !== 'tool_use')
    return null
  const item = extractItem(category.toolUse)
  if (!item)
    return null
  // Non-fileChange tool rows render a tool_result-style body (commandExecution),
  // an alwaysVisible MCP body, or a collapsible collab prompt -- sized via the shared
  // result-body model. webSearch (header-only in its settled single-query form) and
  // anything else route through the generic header estimate.
  const type = pickString(item, 'type')
  if (type === CODEX_ITEM.COMMAND_EXECUTION)
    return codexCommandFields(item, state)
  if (type === CODEX_ITEM.MCP_TOOL_CALL || type === CODEX_ITEM.DYNAMIC_TOOL_CALL)
    return codexMcpFields(item)
  if (type === CODEX_ITEM.COLLAB_AGENT_TOOL_CALL)
    return codexCollabFields(item, state)
  if (type !== CODEX_ITEM.FILE_CHANGE)
    return null
  // Diffs render only on a COMPLETED fileChange; an in-progress row shows a
  // header + live stream, not a diff (renderInProgressFileChange).
  if (pickString(item, 'status') !== CODEX_STATUS.COMPLETED)
    return null

  const shape = buildFileChangeShape(item)
  // A completed fileChange mounts HEADERLESS (ToolResultMessage / a bare toolMessage
  // div, never ToolUseLayout -- renderCompletedFileChange), so every branch flags
  // `toolUseRendersResultBody` to suppress estimateDiffRow / estimateToolUseHeader's
  // phantom tool-title line.
  // A single simple-add renders one all-added diff from its body.
  if (shape.simpleAdd) {
    const fields = diffFieldsFromSources([fileEditDiffFromNewFile(shape.simpleAddPath, shape.simpleAddContent)])
    return fields ? { ...fields, toolUseRendersResultBody: true } : null
  }
  // A simple-delete renders a single header-LESS "Deleted `<path>`" markdown line
  // (ToolResultMessage displayKind="markdown", no status header) -- size that one body
  // row, not estimateToolUseHeader's phantom tool header + summary.
  if (shape.simpleDelete) {
    const line = `Deleted \`${shape.simpleDeletePath}\``
    return { toolUseRendersResultBody: true, ...markdownBody(line) }
  }

  const entries = completedFileChangeEntries(shape.changes, shape.parsedDiffs)
  if (entries.length === 0) {
    // Changes present but none parsed to a diff (and not a simple add/delete): the
    // renderer (renderCompletedFileChange) draws a header-LESS toolMessage with one
    // "N files changed" prompt line. Size that one prompt row (no tool header) instead
    // of falling through to null -> estimateToolUseHeader, which charges a header the
    // render omits. An empty changes set renders a bare div, where the header fallback
    // is acceptable.
    return shape.changes.length > 0
      ? { toolUseRendersResultBody: true, summaryLineCount: 1 }
      : null
  }
  const fields = diffFieldsFromSources(entries.map(e => fileEditDiffFromHunks(e.path, e.hunks)))
  if (!fields)
    return null
  // The renderer labels each file (path + diff-stats badge) above its block when
  // a fileChange spans more than one file (showPerFileLabels); size those rows.
  const headerless = { ...fields, toolUseRendersResultBody: true }
  return entries.length > 1
    ? { ...headerless, diffPerFileLabelRows: entries.length }
    : headerless
}
