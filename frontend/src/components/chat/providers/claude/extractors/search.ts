import type { SearchBodyKind, SearchResult } from '../../../ir/searchResult'
import type { ToolCallPayload } from '../../../ir/toolCall'
import type { GlobRequest } from '../../../ir/tools/glob'
import type { GrepRequest } from '../../../ir/tools/grep'
import type { ClaudeToolRow } from './toolCommon'
import { pickBool, pickNumber, pickString, stringArray } from '~/lib/jsonPick'
import { searchMode } from '../../../ir/searchMode'
import { CLAUDE_TOOL_NAMES } from '../toolNames'
import { claudeFailedResult } from './failure'

/** Grep content-mode line pattern: "line_num:text" or "file:line_num:text". */
const GREP_CONTENT_LINE_RE = /^\d+[:-]|^[^:]+:\d+[:-]/

/**
 * Summary line patterns found at the start of raw Grep/Glob tool output.
 * When tool_use_result is absent (e.g. subagent), the raw text starts with
 * a summary line like "Found 21 files" followed by the actual file list.
 */
const RAW_RESULT_SUMMARY_RE = /^(?:Found (\d+) (?:files?|lines?(?:\s+and\s+\d+\s+files?)?)|(\d+) match(?:es)? in (\d+) files?|No (?:matches|files) found)$/

/**
 * Trailing summary line emitted by Grep `output_mode: "count"`. Per
 * claude-code/src/tools/GrepTool/GrepTool.ts the suffix may include
 * " with pagination = limit: N, offset: N", so we anchor on the prefix only.
 */
const RAW_COUNT_TRAILING_SUMMARY_RE = /^Found (\d+) total occurrences? across (\d+) files?\b/

/**
 * Parse raw Grep/Glob result text (without tool_use_result).
 * Strips the leading or trailing summary line (if any) and returns
 * structured data matching what tool_use_result would provide.
 */
export function parseRawGrepGlobResult(raw: string, toolName: string): {
  numFiles: number
  numLines: number
  numMatches?: number
  mode?: 'count'
  filenames: string[]
  content: string
  /** The raw text STATED that the tool found nothing. See {@link SearchResult.empty}. */
  empty: boolean
} {
  const lines = raw.split('\n')
  const firstLine = lines[0]?.trim() ?? ''
  const summaryMatch = firstLine.match(RAW_RESULT_SUMMARY_RE)

  // Strip the leading summary (if any), then look at non-empty data lines.
  const afterLeading = summaryMatch ? lines.slice(1) : lines
  const nonEmpty = afterLeading.filter(l => l.trim())

  // The one wording the command line interface prints for a search that found
  // nothing. `RAW_RESULT_SUMMARY_RE` already recognizes it: the summary matched and
  // it captured no count, which no other branch of that pattern does. A result with
  // no text at all states the same fact without words. Both need an empty body, so a
  // summary that heads a list stays a list.
  //
  // This parser OWNS the wording, because it owns the pattern that reads it. A
  // caller that spelled the sentence a second time would drift from the pattern.
  const statedNothing = !!summaryMatch && summaryMatch[1] === undefined && summaryMatch[2] === undefined
  const empty = (statedNothing || raw.trim() === '') && nonEmpty.length === 0

  // Count-mode emits the summary on the *last* non-empty line. Detect and
  // strip it before classifying the body.
  const lastLine = nonEmpty[nonEmpty.length - 1]?.trim() ?? ''
  const trailingMatch = lastLine.match(RAW_COUNT_TRAILING_SUMMARY_RE)
  if (trailingMatch) {
    const numMatches = Number.parseInt(trailingMatch[1]!, 10)
    const numFiles = Number.parseInt(trailingMatch[2]!, 10)
    const body = nonEmpty.slice(0, -1)
    return {
      numFiles,
      numLines: 0,
      numMatches,
      mode: 'count',
      filenames: [],
      content: body.join('\n'),
      // Count mode states its own total, so zero occurrences is the tool reporting
      // nothing found rather than a body this parser could not classify.
      empty: numMatches === 0,
    }
  }

  // For Grep content mode (lines contain "file:line:match" or "line_num:text"),
  // we check the first few lines to classify the output format.
  const sampleLines = nonEmpty.length > 5 ? nonEmpty.slice(0, 5) : nonEmpty
  const looksLikeContent = toolName === CLAUDE_TOOL_NAMES.GREP
    && sampleLines.length > 0
    && sampleLines.every(l => GREP_CONTENT_LINE_RE.test(l))

  let numFiles = 0
  let numLines = 0

  if (summaryMatch) {
    if (summaryMatch[1]) {
      // "Found N files" or "Found N lines"
      const n = Number.parseInt(summaryMatch[1], 10)
      if (firstLine.includes('line')) {
        numLines = n
      }
      else {
        numFiles = n
      }
    }
    else if (summaryMatch[2] && summaryMatch[3]) {
      // "N matches in M files"
      numLines = Number.parseInt(summaryMatch[2], 10)
      numFiles = Number.parseInt(summaryMatch[3], 10)
    }
  }

  if (looksLikeContent) {
    return {
      numFiles: numFiles || 0,
      numLines: numLines || nonEmpty.length,
      filenames: [],
      content: nonEmpty.join('\n'),
      empty,
    }
  }

  return {
    numFiles: numFiles || nonEmpty.length,
    numLines: 0,
    filenames: nonEmpty,
    content: '',
    empty,
  }
}

/**
 * Build a SearchResult for a Claude `Grep` or `Glob` tool_result.
 * Branches on `variant` for the structured-result fields (Grep emits content
 * + match counts; Glob emits truncated + durationMs); the subagent fallback
 * (no `tool_use_result`, parse the raw text) is shared.
 */
export function claudeSearchFromToolResult(
  variant: Extract<SearchBodyKind, 'grep' | 'glob'>,
  toolUseResult: Record<string, unknown> | null | undefined,
  resultContent: string,
): SearchResult {
  if (toolUseResult) {
    // `stringArray`, never a cast: `filenames` arrives off the wire, and the file list
    // it feeds runs `relativizePath` over every entry. That helper calls string methods,
    // so one non-string element throws and takes the whole transcript row with it as
    // soon as a working directory is in context.
    const statedFilenames = Array.isArray(toolUseResult.filenames) ? toolUseResult.filenames : null
    const filenames = stringArray(statedFilenames)
    if (variant === 'grep') {
      const content = pickString(toolUseResult, 'content')
      const numFiles = pickNumber(toolUseResult, 'numFiles', 0)
      const numLines = pickNumber(toolUseResult, 'numLines', 0)
      const numMatches = pickNumber(toolUseResult, 'numMatches', undefined)
      const mode = searchMode(toolUseResult.mode)
      // The tool's OWN counters, which is what makes this a RECOGNIZED empty rather
      // than a guess: zero on each one it stated, with no match text and no file, is
      // the tool reporting that it found nothing. A structured object that stated no
      // counter at all is the other case -- a body this build could not read -- so it
      // stays not-empty and the raw text still draws.
      const counted = 'numFiles' in toolUseResult || 'numLines' in toolUseResult || 'numMatches' in toolUseResult
      return {
        filenames,
        content,
        numFiles,
        numLines,
        // Each optional half rides only when the tool stated it.
        ...(numMatches !== undefined ? { matchCount: numMatches } : {}),
        truncated: toolUseResult.appliedLimit != null,
        ...(mode !== undefined ? { mode } : {}),
        fallbackContent: resultContent,
        empty: counted && !content && filenames.length === 0 && numFiles === 0 && numLines === 0 && (numMatches ?? 0) === 0,
      }
    }
    const durationMs = pickNumber(toolUseResult, 'durationMs', undefined)
    return {
      filenames,
      content: '',
      numFiles: filenames.length,
      numLines: 0,
      truncated: pickBool(toolUseResult, 'truncated'),
      ...(durationMs !== undefined ? { durationMs } : {}),
      fallbackContent: resultContent,
      // Glob's structured answer IS its file list, so a list the tool stated and left
      // empty is "no file matched". A result that stated no list did not answer. The
      // RAW length decides it, not the filtered one: a list whose entries this build
      // cannot read stated no empty result either, and the raw text still draws.
      empty: statedFilenames !== null && statedFilenames.length === 0,
    }
  }
  // Subagent: parse raw resultContent.
  const toolName = variant === 'grep' ? CLAUDE_TOOL_NAMES.GREP : CLAUDE_TOOL_NAMES.GLOB
  const raw = parseRawGrepGlobResult(resultContent, toolName)
  if (variant === 'grep') {
    return {
      filenames: raw.filenames,
      content: raw.content,
      numFiles: raw.numFiles,
      numLines: raw.numLines,
      // Each optional half rides only when the raw text stated it.
      ...(raw.numMatches !== undefined ? { matchCount: raw.numMatches } : {}),
      ...(raw.mode !== undefined ? { mode: raw.mode } : {}),
      truncated: false,
      fallbackContent: resultContent,
      empty: raw.empty,
    }
  }
  return {
    filenames: raw.filenames,
    content: '',
    numFiles: raw.numFiles,
    numLines: 0,
    truncated: false,
    fallbackContent: resultContent,
    empty: raw.empty,
  }
}

/**
 * The grep pair: the pattern, its paths, and the mode the tool reported.
 *
 * The failure rung comes BEFORE the parse, and it must. A failed search carries no
 * `tool_use_result`, so the reason drops to the subagent fallback, and
 * `parseRawGrepGlobResult` classifies any line it cannot read as content as a file
 * name -- which drew "File does not exist." as a file hit under the summary
 * "Found 1 file".
 */
export function claudeGrepPayload(request: GrepRequest, result: ClaudeToolRow | undefined): ToolCallPayload<'grep'> {
  if (!result)
    return { kind: 'grep', request }
  const failure = claudeFailedResult(result)
  if (failure)
    return { kind: 'grep', request, result: failure }
  return { kind: 'grep', request, result: claudeSearchFromToolResult('grep', result.toolUseResult, result.resultContent) }
}

/** The glob pair: the pattern and the paths it ran in. The failure rung leads, as above. */
export function claudeGlobPayload(request: GlobRequest, result: ClaudeToolRow | undefined): ToolCallPayload<'glob'> {
  if (!result)
    return { kind: 'glob', request }
  const failure = claudeFailedResult(result)
  if (failure)
    return { kind: 'glob', request, result: failure }
  return { kind: 'glob', request, result: claudeSearchFromToolResult('glob', result.toolUseResult, result.resultContent) }
}
