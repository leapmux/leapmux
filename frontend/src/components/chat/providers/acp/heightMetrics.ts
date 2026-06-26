import type { HeightInput, RowUiState } from '../../chatHeightEstimator'
import type { MessageCategory } from '../../messageClassification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { pickObject, pickString } from '~/lib/jsonPick'
import { normalizedCommandBody, stripLeadingBlankLines } from '~/lib/normalizeProgressOutput'
import { ACP_SESSION_UPDATE, ACP_TOOL_KIND } from '~/types/toolMessages'
import { countLines, markdownBody, monoBody, monoReadBody } from '../../chatHeightShared'
import { diffFieldsFromSource, pickFileEditDiff } from '../../results/fileEditDiff'
import { isMultiLineCommand } from '../../results/multiLineCommandBody'
import { commandResultBodyFields } from '../commandResultHeight'
import { acpExecuteFromToolCall } from './extractors/execute'
import { acpFileEditFromToolCallContent, acpFileEditFromToolCallRawInput } from './extractors/fileEdit'
import { acpReadFromToolCall } from './extractors/read'
import { acpSearchFromToolCall } from './extractors/search'
import { acpWebFetchFromToolCall } from './extractors/webFetch'

/**
 * Shared `Provider.heightMetrics` for every ACP-based provider (opencode, kilo,
 * copilot, cursor, goose, reasonix), mirroring `acpToolCallUpdateRenderer`. A settled
 * `tool_call_update` renders via ToolUseLayout (tool header) + a kind-specific body:
 *
 *  - the DIFF a row renders (result-side `content[]` diff, else the `rawInput`
 *    edit/write fallback); diffs take precedence and a FAILED update synthesizes none.
 *  - EXECUTE: a 1-line command summary (or the full multi-line command when the
 *    OPENCODE_TOOL_CALL_UPDATE toggle is expanded) + the collapsible CommandResultBody
 *    output (TOOL_RESULT_EXPANDED).
 *  - READ: the collapsible cat-n code body.
 *  - SEARCH: the "Found N matches" summary line, else a collapsible mono fallback.
 *  - FETCH: the summary line + collapsible markdown body (rare -- needs a numeric code).
 *
 * The initial `tool_call` (pending) row is header-only and returns null.
 */
export function acpHeightMetrics(
  category: MessageCategory,
  _parsed: ParsedMessageContent,
  _toolUseParsed: ParsedMessageContent | undefined,
  state: RowUiState,
): Partial<HeightInput> | null {
  if (category.kind !== 'tool_use')
    return null
  const toolUse = category.toolUse
  // Diffs / kind bodies render only on a tool_call_update (acpToolCallUpdateRenderer);
  // the initial tool_call is a header-only pending row.
  if (toolUse.sessionUpdate !== ACP_SESSION_UPDATE.TOOL_CALL_UPDATE)
    return null
  const kind = typeof toolUse.kind === 'string' ? toolUse.kind : undefined
  const rawInput = pickObject(toolUse, 'rawInput') ?? undefined
  // A diff takes precedence -- but NOT on failure, where rawInput is the attempted
  // (not applied) edit and the renderer shows error text rather than a synthesized diff.
  if (toolUse.status !== 'failed') {
    const diff = diffFieldsFromSource(pickFileEditDiff(
      acpFileEditFromToolCallContent(toolUse.content),
      acpFileEditFromToolCallRawInput(kind, rawInput),
    ))
    if (diff)
      return diff
  }
  // The kind-specific body (CommandResultBody / ReadFileResultBody / SearchResultBody /
  // WebFetchResultBody) renders on success AND failure, so size it regardless of status.
  if (kind === ACP_TOOL_KIND.EXECUTE)
    return acpExecuteFields(toolUse, rawInput, state)
  if (kind === ACP_TOOL_KIND.READ)
    return acpReadFields(toolUse, state)
  if (kind === ACP_TOOL_KIND.SEARCH)
    return acpSearchFields(toolUse, state)
  if (kind === ACP_TOOL_KIND.FETCH)
    return acpFetchFields(toolUse, state)
  return null
}

/**
 * ACP `execute`: tool header + the command (a 1-line summary, or the full multi-line
 * command when its OPENCODE_TOOL_CALL_UPDATE toggle is expanded -- the never-measured
 * default is collapsed) + the collapsible CommandResultBody output (TOOL_RESULT_EXPANDED),
 * with a status header on a non-Success result.
 */
function acpExecuteFields(toolUse: Record<string, unknown>, rawInput: Record<string, unknown> | undefined, state: RowUiState): Partial<HeightInput> | null {
  const source = acpExecuteFromToolCall(toolUse)
  if (!source)
    return null
  const command = pickString(rawInput, 'command')
  const body = normalizedCommandBody(source.output)
  // The command shows as a 1-line summary by default; the OPENCODE toggle (resolved as
  // toolBodyExpanded) reveals the full multi-line command above the output.
  const commandRows = command ? (state.toolBodyExpanded && isMultiLineCommand(command) ? countLines(command) : 1) : 0
  // The shared command-result body geometry, plus ACP's tool header + command summary.
  return {
    ...commandResultBodyFields(source, body, state.collapsed),
    toolHeaderLine: true,
    summaryLineCount: commandRows,
  }
}

/**
 * The shared shape of a collapsible ACP result body: the tool-header + result-body
 * flags wrapping a body's text metrics, with the row's collapsed state last. `bodyFields`
 * carries the body sizing (a `monoBody`/`markdownBody` spread, plus any summary line),
 * and `collapsed` always wins so a body builder's own flag can't leak through. One home
 * for the read / search-fallback / fetch literals so their flag set can't drift.
 */
function acpCollapsibleBody(bodyFields: Partial<HeightInput>, state: RowUiState): Partial<HeightInput> {
  return {
    toolUseRendersResultBody: true,
    toolHeaderLine: true,
    ...bodyFields,
    collapsed: state.collapsed,
  }
}

/**
 * ACP `read`: tool header + the collapsible cat-n code body (ReadFileResultBody). When
 * the output didn't parse as cat-n (no `lines`) the renderer does NOT fall to a bare
 * header -- its `body().kind === 'none'` branch still mounts a collapsible ansi-or-pre
 * <pre> of the raw output (toolCallUpdate.tsx), sized exactly like a mono tool_result
 * body. So size THAT (mirroring the renderer's stripLeadingBlankLines(outputText));
 * only a genuinely empty output is a true header-only row. The leading/trailing
 * reminder alerts render only when expanded, so the collapsed default is body-only.
 */
function acpReadFields(toolUse: Record<string, unknown>, state: RowUiState): Partial<HeightInput> | null {
  const source = acpReadFromToolCall(toolUse)
  if (!source)
    return null
  if (source.lines === null) {
    const fallback = stripLeadingBlankLines(source.fallbackContent)
    if (!fallback)
      return null
    return acpCollapsibleBody(monoBody(fallback), state)
  }
  // Size from the PARSED cat-n lines the renderer draws, NOT the raw fallbackContent.
  // ReadFileResultBody collapses on items().length (= source.lines.length) AFTER
  // parseReadContent strips the <reminder> blocks, so charging the raw text's line count
  // (reminders counted inline) trips the collapse gate at the wrong row count: a read
  // whose PARSED body is under the collapse threshold but whose raw text (+ a reminder
  // block) is over it reads as collapsed in the estimate while the renderer draws it in
  // full -- a large under-estimate. The reminder alerts render only when expanded, so the
  // collapsed-default body the estimate targets is exactly these lines.
  return acpCollapsibleBody(monoReadBody(source.lines), state)
}

/**
 * ACP `search`: tool header + a "Found N matches" summary line (SearchResultBody) when
 * the result carries a match count, else the collapsible mono fallback body.
 */
function acpSearchFields(toolUse: Record<string, unknown>, state: RowUiState): Partial<HeightInput> | null {
  const source = acpSearchFromToolCall(toolUse)
  if (!source)
    return null
  if (source.matches !== undefined)
    return { toolUseRendersResultBody: true, toolHeaderLine: true, summaryLineCount: 1 }
  return acpCollapsibleBody(monoBody(source.fallbackContent), state)
}

/**
 * ACP `fetch`: tool header + a "code text - bytes" summary line + the collapsible
 * markdown body (WebFetchResultBody). Null unless the raw output carries a numeric
 * `code` (the renderer's own gate), in which case the generic header sizing applies.
 */
function acpFetchFields(toolUse: Record<string, unknown>, state: RowUiState): Partial<HeightInput> | null {
  const source = acpWebFetchFromToolCall(toolUse)
  if (!source)
    return null
  return acpCollapsibleBody({ summaryLineCount: 1, ...markdownBody(source.result) }, state)
}
