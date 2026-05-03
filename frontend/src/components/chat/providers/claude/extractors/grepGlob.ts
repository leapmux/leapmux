import type { SearchResultSource } from '../../../results/searchResult'
import { pickBool, pickNumber, pickString } from '~/lib/jsonPick'
import { CLAUDE_TOOL } from '~/types/toolMessages'

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
} {
  const lines = raw.split('\n')
  const firstLine = lines[0]?.trim() ?? ''
  const summaryMatch = firstLine.match(RAW_RESULT_SUMMARY_RE)

  // Strip the leading summary (if any), then look at non-empty data lines.
  const afterLeading = summaryMatch ? lines.slice(1) : lines
  const nonEmpty = afterLeading.filter(l => l.trim())

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
    }
  }

  // For Grep content mode (lines contain "file:line:match" or "line_num:text"),
  // we check the first few lines to classify the output format.
  const sampleLines = nonEmpty.length > 5 ? nonEmpty.slice(0, 5) : nonEmpty
  const looksLikeContent = toolName === CLAUDE_TOOL.GREP
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
    }
  }

  return {
    numFiles: numFiles || nonEmpty.length,
    numLines: 0,
    filenames: nonEmpty,
    content: '',
  }
}

/**
 * Build a SearchResultSource for a Claude `Grep` or `Glob` tool_result.
 * Branches on `variant` for the structured-result fields (Grep emits content
 * + match counts; Glob emits truncated + durationMs); the subagent fallback
 * (no `tool_use_result`, parse the raw text) is shared.
 */
export function claudeSearchFromToolResult(
  variant: 'grep' | 'glob',
  toolUseResult: Record<string, unknown> | null | undefined,
  resultContent: string,
): SearchResultSource {
  if (toolUseResult) {
    const filenames = Array.isArray(toolUseResult.filenames) ? toolUseResult.filenames as string[] : []
    if (variant === 'grep') {
      return {
        variant: 'grep',
        filenames,
        content: pickString(toolUseResult, 'content'),
        numFiles: pickNumber(toolUseResult, 'numFiles', 0),
        numLines: pickNumber(toolUseResult, 'numLines', 0),
        numMatches: pickNumber(toolUseResult, 'numMatches', undefined),
        truncated: toolUseResult.appliedLimit != null,
        mode: pickString(toolUseResult, 'mode', undefined),
        fallbackContent: resultContent,
      }
    }
    return {
      variant: 'glob',
      filenames,
      content: '',
      numFiles: filenames.length,
      numLines: 0,
      truncated: pickBool(toolUseResult, 'truncated'),
      durationMs: pickNumber(toolUseResult, 'durationMs', undefined),
      fallbackContent: resultContent,
    }
  }
  // Subagent: parse raw resultContent.
  const toolName = variant === 'grep' ? CLAUDE_TOOL.GREP : CLAUDE_TOOL.GLOB
  const raw = parseRawGrepGlobResult(resultContent, toolName)
  if (variant === 'grep') {
    return {
      variant,
      filenames: raw.filenames,
      content: raw.content,
      numFiles: raw.numFiles,
      numLines: raw.numLines,
      numMatches: raw.numMatches,
      mode: raw.mode,
      truncated: false,
      fallbackContent: resultContent,
    }
  }
  return {
    variant,
    filenames: raw.filenames,
    content: '',
    numFiles: raw.numFiles,
    numLines: 0,
    truncated: false,
    fallbackContent: resultContent,
  }
}

/** Build a SearchResultSource for a Claude `Grep` tool_result. */
export function claudeGrepFromToolResult(
  toolUseResult: Record<string, unknown> | null | undefined,
  resultContent: string,
): SearchResultSource {
  return claudeSearchFromToolResult('grep', toolUseResult, resultContent)
}

/** Build a SearchResultSource for a Claude `Glob` tool_result. */
export function claudeGlobFromToolResult(
  toolUseResult: Record<string, unknown> | null | undefined,
  resultContent: string,
): SearchResultSource {
  return claudeSearchFromToolResult('glob', toolUseResult, resultContent)
}
