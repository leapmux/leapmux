import type { ReadFileResultSource } from '../../../results/readFileResult'
import type { ParsedCatLine } from '../../../results/ReadResultView'
import { isObject, pickNumber, pickString } from '~/lib/jsonPick'
import { parseCatNContent } from '../../../results/ReadResultView'

/** Non-text Read variants that the catch-all renderer continues to handle. */
const NON_TEXT_READ_TYPES = new Set(['image', 'notebook', 'pdf', 'parts', 'file_unchanged'])

interface ClaudeReadInputArg {
  toolUseResult?: Record<string, unknown> | null
  resultContent: string
  toolInput?: Record<string, unknown> | null
}

/**
 * Build a ReadFileResultSource from a Claude `Read` tool_result. Returns null
 * for non-text variants (image/notebook/pdf/parts/file_unchanged), letting
 * downstream renderers fall back to their existing handling for those.
 *
 * For text variants the structured `tool_use_result.file` payload is preferred;
 * otherwise the raw `resultContent` is parsed as cat-n format (subagent
 * fallback). When neither parses, `lines` is null and the body renders
 * `resultContent` as plain text.
 */
export function claudeReadFromToolResult(args: ClaudeReadInputArg): ReadFileResultSource | null {
  const { toolUseResult, resultContent, toolInput } = args

  const variantType = pickString(toolUseResult, 'type')
  if (variantType && NON_TEXT_READ_TYPES.has(variantType))
    return null

  const file = isObject(toolUseResult?.file) ? toolUseResult!.file as Record<string, unknown> : null

  if (file) {
    const filePath = pickString(file, 'filePath')
    const fileContent = pickString(file, 'content')
    const startLine = pickNumber(file, 'startLine', 1)
    const totalLines = pickNumber(file, 'totalLines', 0)
    const numLines = pickNumber(file, 'numLines', 0)
    const lines: ParsedCatLine[] = fileContent
      ? fileContent.split('\n').map((text, i) => ({ num: startLine + i, text }))
      : []
    return {
      filePath,
      lines,
      totalLines,
      numLines,
      fallbackContent: resultContent,
    }
  }

  // Subagent fallback: try to parse the raw resultContent as cat-n.
  const parsedLines = parseCatNContent(resultContent)
  const filePath = pickString(toolInput, 'file_path')
  return {
    filePath,
    lines: parsedLines,
    totalLines: 0,
    numLines: 0,
    fallbackContent: resultContent,
  }
}
