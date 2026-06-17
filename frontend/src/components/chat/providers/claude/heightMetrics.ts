import type { HeightInput, RowUiState } from '../../chatHeightEstimator'
import type { MessageCategory } from '../../messageClassification'
import type { FileEditDiffSource } from '../../results/fileEditDiff'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { prettifyArgsJson, prettifyJson, prettifyStructuredJson } from '~/lib/jsonFormat'
import { isObject, pickString, stringArray } from '~/lib/jsonPick'
import { normalizedCommandBody, PROGRESS_MAX_ROWS } from '~/lib/normalizeProgressOutput'
import { CLAUDE_TOOL } from '~/types/toolMessages'
import { countLines, firstToolResultBlockText, isToolResultError, markdownBody, messageContentBlocks, monoBody, monoReadBody, toLineLengths } from '../../chatHeightShared'
import { diffFieldsFromSource, pickFileEditDiff } from '../../results/fileEditDiff'
import { extractToolUseInfo } from './extractors/assistantContent'
import { claudeBashFromToolResult } from './extractors/bash'
import { claudeCreateResultDiff, claudeFileEditFromToolUseInput, claudeFileEditFromToolUseResult, isClaudeFileEditTool } from './extractors/fileEdit'
import { claudeSearchFromToolResult } from './extractors/grepGlob'
import { isClaudeMcpTool } from './extractors/mcp'
import { claudeReadFromToolResult } from './extractors/read'
import { claudeRemoteTriggerFromToolResult } from './extractors/remoteTrigger'
import { claudeWebFetchFromToolResult } from './extractors/webFetch'
import { claudeWebSearchFromToolResult } from './extractors/webSearch'

/**
 * Claude's `Provider.heightMetrics`: the provider-specific slice of a row's
 * pre-mount height input that the shared `buildHeightInput` can't read from a
 * neutral structure. Three responsibilities, all keyed on Claude's
 * `tool_use_result` / `result` wire shapes:
 *
 *  - tool_result DIFF geometry (Edit/Write), mirroring the renderer's
 *    `pickFileEditDiff(result, toolUseInput)` (toolResults/index.tsx) so a tall
 *    edit is sized as a diff, not clamped text.
 *  - tool_result `bodyMarkdown` (Agent/WebFetch/MCP render 14px markdown) and
 *    `hasHeader` (a sub-agent/task result or an interrupted result wraps in a
 *    ToolStatusHeader). The orchestrator ORs the generic `isError` into hasHeader.
 *  - `result_divider` error detail (the `<pre>` block under the divider line).
 */
export function claudeHeightMetrics(
  category: MessageCategory,
  parsed: ParsedMessageContent,
  toolUseParsed: ParsedMessageContent | undefined,
  _state: RowUiState,
): Partial<HeightInput> | null {
  if (category.kind === 'tool_result') {
    // Resolve the result shape, the paired tool_use opener info, and the tool name
    // ONCE here -- claudeDiffFields and claudeCustomResultFields both need them, and
    // extractToolUseInfo (an unmemoized walk of the opener's content) would otherwise
    // run twice on the common non-edit path (diff resolves the name to return null,
    // then the custom dispatch re-derives it).
    const tur = toolUseResult(parsed)
    const toolUseInfo = toolUseParsed ? extractToolUseInfo(toolUseParsed) : null
    const toolName = pickString(tur, 'tool_name') || toolUseInfo?.toolName || ''
    const isError = isToolResultError(parsed)

    const diff = claudeDiffFields(tur, toolUseInfo, toolName, isError)
    if (diff)
      return diff
    // An AskUserQuestion result renders one compact `header: answer` row per
    // question (AskUserQuestionResultView), NOT the raw "Your questions have been
    // answered: ..." content string the generic text path would size.
    const ask = claudeAskAnswerFields(parsed)
    if (ask)
      return ask
    // A custom result renderer (Grep/Glob, ToolSearch, WebSearch, WebFetch,
    // RemoteTrigger, TaskOutput, Bash, ExitPlanMode, MCP) draws something OTHER
    // than the raw tool_result content string the generic text model sizes --
    // structured lists, a different wire field, a ToolStatusHeader, a summary
    // prompt, an uncollapsed pre. Route each to the extractor that mirrors what
    // its bespoke view actually draws so the off-screen estimate matches.
    const custom = claudeCustomResultFields(toolName, tur, parsed, toolUseParsed, isError)
    if (custom)
      return custom
    // No diff/ask/custom: carry the body-style flags the estimator needs to size
    // the tool_result body (markdown line height + block gaps; leading status header).
    return {
      bodyMarkdown: isMarkdownResult(parsed),
      hasHeader: isAgentOrTaskResult(parsed) || isInterruptedResult(parsed),
    }
  }

  if (category.kind === 'result_divider') {
    const detail = extractResultDividerDetail(parsed)
    if (detail)
      // Per-hard-line lengths so estimateSingleLineMeta sums each line's wrap: the
      // resultErrorDetail <pre> is pre-wrap, so each errors[] entry wraps on its own
      // and the flat total-wrap model under-counts several long error lines.
      return { textLength: detail.length, logicalLineCount: countLines(detail), lineLengths: toLineLengths(detail) }
    return null
  }

  return null
}

/**
 * The diff for a Claude file-edit tool_RESULT row, sized exactly as the renderer
 * draws it (FileEditDiffBody, headerless): the result-side `tool_use_result`
 * diff, else the paired tool_use INPUT's diff (claudeFileEditFromToolUseInput),
 * else -- when the sibling is absent -- a `Write`-create's own result `content`
 * synthesized as an all-added diff. Returns null when the row carries no
 * renderable diff (incl. a failed edit, which renders its error text instead).
 */
function claudeDiffFields(
  tur: Record<string, unknown> | undefined,
  toolUseInfo: { toolName: string, input: Record<string, unknown> } | null,
  toolName: string,
  isError: boolean,
): Partial<HeightInput> | null {
  if (!tur || isError)
    return null
  // Only Edit/Write results render a diff (the renderer's `isClaudeFileEditTool`
  // gate). `tool_name` is often absent (the renderer falls back to span type,
  // which the estimator can't read), so an absent name is taken as the dominant
  // Edit case; a present non-edit name renders as text.
  if (toolName && !isClaudeFileEditTool(toolName))
    return null

  // pickFileEditDiff mirrors the renderer (result-side diff, else the paired
  // tool_use INPUT's diff); claudeCreateResultDiff adds the create-with-no-sibling
  // fallback the renderer ALSO applies now -- the shared helper keeps the estimate
  // and the render in lockstep (we already returned above on isToolResultError).
  const source: FileEditDiffSource | null = pickFileEditDiff(
    claudeFileEditFromToolUseResult(tur),
    toolUseInfo ? claudeFileEditFromToolUseInput(toolUseInfo.toolName, toolUseInfo.input) : null,
  ) ?? claudeCreateResultDiff(tur, false)
  return diffFieldsFromSource(source)
}

/**
 * The per-answer row lengths for an AskUserQuestion tool_RESULT, sized exactly as
 * AskUserQuestionResultView draws it: one `header: answer` row per question (header
 * = `q.header || q.question`, answer keyed by the full question text, else the
 * header, else "Not answered"). Null when the result carries no questions array (so
 * the row falls back to generic text sizing). Mirrors askUserQuestion.tsx.
 */
function claudeAskAnswerFields(parsed: ParsedMessageContent): Partial<HeightInput> | null {
  const tur = toolUseResult(parsed)
  if (!tur || !Array.isArray(tur.questions) || tur.questions.length === 0)
    return null
  const answers = isObject(tur.answers) ? tur.answers : {}
  const askAnswerLineLengths = tur.questions.map((q) => {
    if (!isObject(q))
      return 0
    const question = pickString(q, 'question')
    const header = pickString(q, 'header') || question
    const answer = pickString(answers, question) || pickString(answers, header) || 'Not answered'
    return `${header}: ${answer}`.length
  })
  return { askAnswerLineLengths }
}

/**
 * Dispatch a tool_result row to the custom-renderer height fields for its tool,
 * or null to fall back to the generic body/header model.
 *
 * Grep/Glob are handled REGARDLESS of isError: SearchResultBody is dispatched
 * unconditionally (toolResults/index.tsx), so an errored Grep/Glob still renders
 * that view (a header-less mono fallback body), NOT the catch-all error path -- so
 * the generic isError model would wrongly charge a status header it never draws.
 *
 * Every other tool is gated on `!isError`: those renderers' dispatch guards return
 * null on the typical error shape (absent structured payload), so the row renders
 * via the catch-all ToolResultMessage -- a leading "Error" header + the full error
 * text, which the generic isError path already sizes. ExitPlanMode's feedback
 * (is_error) case is one of those by design: its always-present "Sent feedback:"
 * header is charged by the generic isError -> hasHeader merge, and its MarkdownText
 * body is sized as markdown via isMarkdownResult (which now matches ExitPlanMode).
 */
function claudeCustomResultFields(
  toolName: string,
  tur: Record<string, unknown> | undefined,
  parsed: ParsedMessageContent,
  toolUseParsed: ParsedMessageContent | undefined,
  isError: boolean,
): Partial<HeightInput> | null {
  if (toolName === CLAUDE_TOOL.GREP)
    return claudeSearchHeightFields('grep', tur, parsed, isError)
  if (toolName === CLAUDE_TOOL.GLOB)
    return claudeSearchHeightFields('glob', tur, parsed, isError)
  if (isError)
    return null
  switch (toolName) {
    case CLAUDE_TOOL.READ:
      return claudeReadHeightFields(tur, parsed)
    case CLAUDE_TOOL.TOOL_SEARCH:
      return claudeToolSearchFields(tur)
    case CLAUDE_TOOL.WEB_SEARCH:
      return claudeWebSearchHeightFields(tur)
    case CLAUDE_TOOL.WEB_FETCH:
      return claudeWebFetchHeightFields(tur, parsed)
    case CLAUDE_TOOL.REMOTE_TRIGGER:
      return claudeRemoteTriggerHeightFields(tur, parsed)
    case CLAUDE_TOOL.TASK_OUTPUT:
      return claudeTaskOutputHeightFields(tur)
    case CLAUDE_TOOL.BASH:
      return claudeBashHeightFields(tur, parsed)
    case CLAUDE_TOOL.EXIT_PLAN_MODE:
      return claudeExitPlanModeHeightFields(tur)
  }
  if (isClaudeMcpTool(toolName))
    return claudeMcpHeightFields(tur, toolUseParsed)
  return null
}

/**
 * The joined text of a tool_result's content block(s) -- the same `resultContent`
 * the dispatcher passes to the extractors, so a structured extractor can use its
 * raw-text fallback. Both the content-block read and the per-block join are the
 * SHARED units (messageContentBlocks / firstToolResultBlockText in chatHeightShared,
 * registry-free), so this can no longer drift from `extractText`'s tool_result
 * branch (chatHeightInput.ts).
 */
function resultContentText(parsed: ParsedMessageContent): string {
  return firstToolResultBlockText(messageContentBlocks(parsed) ?? [])
}

/**
 * Claude `Read`: size the collapsible cat-n body from the PARSED lines the renderer
 * draws (ReadFileResultBody collapses on source.lines.length AFTER parseReadContent
 * strips the leading/trailing <reminder> tag blocks), NOT the raw resultContent.
 * Charging the raw line count (reminder + blank lines counted inline) trips the
 * collapse gate at the wrong row count: a read whose parsed body is under the
 * collapse threshold but whose raw text (+ a reminder block) is over it reads as
 * collapsed in the estimate while the renderer draws it in full -- a large
 * under-estimate. Mirrors acpReadFields (acp/heightMetrics). Returns null for a
 * non-text variant (image/notebook/pdf/parts/file_unchanged) or output that didn't
 * parse as cat-n (no `lines`), where the renderer falls back to its raw text body
 * that the generic sizing already covers.
 */
function claudeReadHeightFields(
  tur: Record<string, unknown> | undefined,
  parsed: ParsedMessageContent,
): Partial<HeightInput> | null {
  const source = claudeReadFromToolResult({ toolUseResult: tur ?? null, resultContent: resultContentText(parsed) })
  if (!source || source.lines === null)
    return null
  // The reminder alerts render only when expanded, so the collapsed-default body the
  // estimate targets is exactly these parsed lines (sized mono, like the renderer).
  return monoReadBody(source.lines)
}

/**
 * ToolSearch renders `tool_use_result.matches.join('\n')` in a bare <pre> with NO
 * collapse (toolSearch.tsx) -- the matches are a structured field absent from the
 * tool_result content string the generic path sizes (it sees an empty body -> the
 * 1-row floor). Size the matches as uncollapsed mono. Null without a result payload
 * (the dispatcher then renders the catch-all over the raw content).
 */
function claudeToolSearchFields(tur: Record<string, unknown> | undefined): Partial<HeightInput> | null {
  if (!tur)
    return null
  const matches = stringArray(tur.matches)
  const body = matches.length > 0 ? matches.join('\n') : 'No tools found'
  return { ...monoBody(body), hasHeader: false, uncollapsed: true }
}

/**
 * WebSearch renders a "N results" summary + one never-wrapping link row per result
 * (WebSearchResultsBody), built from `tool_use_result.results` -- not the (empty)
 * content string. Emit the link count (-> estimateWebSearchResult) plus the summary
 * text metrics for its expanded markdown. Null when there are no links (the renderer
 * draws nothing) or no result payload.
 */
function claudeWebSearchHeightFields(tur: Record<string, unknown> | undefined): Partial<HeightInput> | null {
  const source = tur ? claudeWebSearchFromToolResult(tur) : null
  if (!source || source.links.length === 0)
    return null
  return { webSearchLinkCount: source.links.length, ...markdownBody(source.summary) }
}

/**
 * WebFetch renders a summary prompt ("200 OK -- 1.2 KB") + a markdown body sourced
 * from `tool_use_result.result` (webFetchResult.tsx) -- the content block is empty,
 * so the generic path sizes ~nothing. Size the real page body as markdown plus the
 * always-present summary line. Null when the payload carries no numeric `code` (the
 * renderer falls through to the catch-all).
 */
function claudeWebFetchHeightFields(tur: Record<string, unknown> | undefined, parsed: ParsedMessageContent): Partial<HeightInput> | null {
  const source = claudeWebFetchFromToolResult(tur, resultContentText(parsed))
  if (!source)
    return null
  return { summaryLineCount: 1, ...markdownBody(source.result) }
}

/**
 * RemoteTrigger renders a ToolStatusHeader ("HTTP {status}") + a shiki-highlighted
 * pretty-JSON body (remoteTrigger.tsx) -- the renderer explodes the minified wire
 * JSON into many lines the generic path (sizing the 2-line "HTTP n\n{json}" string)
 * never sees, and never charges the header. Size the PRETTY JSON as a json body
 * (jsonLinePx) plus the header. Null without a recognizable payload.
 */
function claudeRemoteTriggerHeightFields(tur: Record<string, unknown> | undefined, parsed: ParsedMessageContent): Partial<HeightInput> | null {
  const source = claudeRemoteTriggerFromToolResult(tur, resultContentText(parsed))
  if (!source)
    return null
  const pretty = prettifyJson(source.parsed ?? source.json)
  return { hasHeader: true, jsonBody: true, ...monoBody(pretty) }
}

/**
 * TaskOutput renders a ToolStatusHeader + the captured `tool_use_result.task.output`
 * (taskOutput.tsx) -- a different wire field than the content block the generic path
 * sizes, so both the body height AND the collapse gate would route off the wrong
 * string. Size the real output as mono with a header. Null when there's no task /
 * output (the generic path then sizes the content block, still with a header via
 * isAgentOrTaskResult, mirroring the renderer's `task.output ?? fallbackContent`).
 */
function claudeTaskOutputHeightFields(tur: Record<string, unknown> | undefined): Partial<HeightInput> | null {
  const task = tur?.task
  if (!isObject(task))
    return null
  const output = pickString(task, 'output')
  if (!output)
    return null
  return { ...monoBody(output), hasHeader: true }
}

/**
 * Bash renders `normalizeProgressOutput(stdout\nstderr)` + leading-blank-stripped
 * (CommandResultBody) -- a `\r`-progress run that is ONE wire line expands into up
 * to PROGRESS_MAX_ROWS displayed lines, and the renderer widens its collapse
 * threshold to match. Size the SAME normalized string the renderer draws, and widen
 * the collapse threshold when the body carried carriage returns. Always applies
 * (Bash always routes through CommandResultBody).
 */
function claudeBashHeightFields(tur: Record<string, unknown> | undefined, parsed: ParsedMessageContent): Partial<HeightInput> {
  const source = claudeBashFromToolResult({ toolUseResult: tur, resultContent: resultContentText(parsed), isError: false })
  const body = normalizedCommandBody(source.output)
  const fields: Partial<HeightInput> = { ...monoBody(body.text), hasHeader: source.interrupted === true }
  if (body.hadCarriageReturns)
    fields.collapsedRowThreshold = PROGRESS_MAX_ROWS
  return fields
}

/**
 * Grep/Glob render a summary prompt ("N matches in M files") + the grep content
 * blob or the structured file list (SearchResultBody) -- for the common structured
 * case the content block is empty/just-the-summary while the files live in
 * `tool_use_result.filenames`. Size the body the renderer actually draws plus the
 * summary line. Always applies (SearchResultBody is always rendered for Grep/Glob).
 *
 * On is_error SearchResultBody draws its header-less mono FALLBACK body (the error
 * text) with no summary line (summaryFor() yields '' for error text) -- size that,
 * not the structured success layout. (The generic isError -> hasHeader merge still
 * charges a header SearchResultBody never draws, but the mono body keeps the total
 * a safe, sub-WARN over-estimate vs the prior 14px/headered generic sizing.)
 */
function claudeSearchHeightFields(variant: 'grep' | 'glob', tur: Record<string, unknown> | undefined, parsed: ParsedMessageContent, isError: boolean): Partial<HeightInput> {
  const content = resultContentText(parsed)
  if (isError)
    return monoBody(content)
  const source = claudeSearchFromToolResult(variant, tur, content)
  const body = source.content || source.filenames.join('\n')
  return { ...monoBody(body), summaryLineCount: 1 }
}

/**
 * ExitPlanMode's APPROVAL result (non-error) draws a ToolStatusHeader ("Plan
 * approved") and, when a plan file was written, a "Plan file: <path>" prompt -- no
 * content body (exitPlanMode.tsx). Charge the always-present header (the generic
 * path omits it for the success case) + the optional prompt line, and zero the body.
 */
function claudeExitPlanModeHeightFields(tur: Record<string, unknown> | undefined): Partial<HeightInput> {
  const filePath = tur ? pickString(tur, 'filePath') : ''
  return { hasHeader: true, summaryLineCount: filePath ? 1 : 0, textLength: 0, logicalLineCount: 0, lineLengths: [] }
}

/**
 * MCP (mcp__server__tool) renders McpToolCallBody, which bypasses the collapse
 * machinery entirely (uncollapsed) and draws always-visible "Arguments" / optional
 * "Structured" pre-blocks the generic path never reads (they come from the tool_use
 * INPUT / `structuredContent`, not the content block). Keep the markdown content
 * body (sized by the generic extractText), mark it uncollapsed + header-less, and
 * surface the args/structured line counts.
 */
function claudeMcpHeightFields(tur: Record<string, unknown> | undefined, toolUseParsed: ParsedMessageContent | undefined): Partial<HeightInput> {
  const info = toolUseParsed ? extractToolUseInfo(toolUseParsed) : null
  const fields: Partial<HeightInput> = { bodyMarkdown: true, hasHeader: false, uncollapsed: true }
  const argsJson = prettifyArgsJson(info?.input)
  if (argsJson)
    fields.argsLineCount = countLines(argsJson)
  const structuredJson = prettifyStructuredJson(tur?.structuredContent)
  if (structuredJson)
    fields.structuredLineCount = countLines(structuredJson)
  return fields
}

/**
 * The `tool_use_result` payload hanging off a parsed Claude user message,
 * narrowed to an object (or undefined when absent / not an object).
 */
function toolUseResult(parsed: ParsedMessageContent): Record<string, unknown> | undefined {
  const tur = isObject(parsed.parentObject) ? parsed.parentObject.tool_use_result : undefined
  return isObject(tur) ? tur : undefined
}

/**
 * True for a sub-agent (`Agent`/Task) or `TaskOutput` result, whose bodies
 * always wrap in a ToolStatusHeader -- unlike Read/Grep/Glob/command results,
 * which render body-only. Detected by the tool_use_result shape (`agentId` for
 * Agent, `task` for TaskOutput) since the tool name isn't carried on the
 * classified tool_result category.
 */
function isAgentOrTaskResult(parsed: ParsedMessageContent): boolean {
  const tur = toolUseResult(parsed)
  if (!tur)
    return false
  return typeof tur.agentId === 'string' || isObject(tur.task)
}

/**
 * True when a Claude command result was interrupted (Ctrl-C). The renderer shows
 * an "Interrupted" ToolStatusHeader for it, but the tool_result block's
 * `is_error` can be false -- the interrupted flag lives on the `tool_use_result`
 * payload, so `isToolResultError` alone misses it.
 */
function isInterruptedResult(parsed: ParsedMessageContent): boolean {
  return toolUseResult(parsed)?.interrupted === true
}

/**
 * True when a tool_result renders its body as 14px MARKDOWN rather than 12px
 * monospace: a sub-agent (`Agent`) result, a `WebFetch` page, an MCP tool's text
 * blocks (`mcp__*`), or an ExitPlanMode FEEDBACK (is_error) body -- the only path
 * that reaches here for ExitPlanMode, since the approval case is handled earlier by
 * claudeExitPlanModeHeightFields. ExitPlanModeResultView draws that feedback via
 * MarkdownText (14px markdown + block gaps), so it must wrap as prose, not mono.
 * Detected from the tool_use_result shape/name -- note `TaskOutput` (carries `task`)
 * renders ANSI/pre, NOT markdown, so it is excluded here even though
 * `isAgentOrTaskResult` covers it for the status-header check.
 */
function isMarkdownResult(parsed: ParsedMessageContent): boolean {
  const tur = toolUseResult(parsed)
  if (!tur)
    return false
  if (typeof tur.agentId === 'string')
    return true
  const tn = pickString(tur, 'tool_name')
  return tn === 'WebFetch' || tn === CLAUDE_TOOL.EXIT_PLAN_MODE || tn.startsWith('mcp__')
}

/**
 * The error-detail text an error result_divider renders in its `<pre>`: the
 * `errors[]` array joined, else the `result` string -- but ONLY for a failed,
 * non-`success` subtype (a generic error bakes its message into the label and
 * shows no detail). Mirrors `buildErrorResult` in providers/claude/resultDivider.tsx.
 */
function extractResultDividerDetail(parsed: ParsedMessageContent): string {
  const tl = parsed.topLevel
  if (!isObject(tl) || tl.type !== 'result' || tl.is_error !== true)
    return ''
  const subtype = typeof tl.subtype === 'string' ? tl.subtype : ''
  if (!subtype || subtype === 'success')
    return ''
  const errors = stringArray(tl.errors)
  if (errors.length > 0)
    return errors.join('\n')
  return typeof tl.result === 'string' ? tl.result : ''
}
