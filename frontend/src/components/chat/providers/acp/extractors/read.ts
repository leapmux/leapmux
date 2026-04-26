import type { ReadFileResultSource } from '../../../results/readFileResult'
import { pickFirstString, pickObject } from '~/lib/jsonPick'
import { parseCatNContent } from '../../../results/ReadResultView'
import { ACP_FILE_PATH_KEYS, collectAcpToolText } from '../rendering'

/**
 * Build a ReadFileResultSource from an ACP `tool_call_update` of kind `read`.
 * Returns null when neither a filePath nor parseable cat-n output is present —
 * letting the caller fall back to the generic text branch.
 *
 * When the raw output parses as cat-n format, `lines` is populated so the
 * shared body renders the syntax-highlighted view. Otherwise `lines` is null
 * and the body shows the raw text via `fallbackContent`.
 */
export function acpReadFromToolCall(toolUse: Record<string, unknown> | null | undefined): ReadFileResultSource | null {
  if (!toolUse)
    return null

  const rawInput = pickObject(toolUse, 'rawInput')
  const filePath = pickFirstString(rawInput, ACP_FILE_PATH_KEYS) ?? ''

  const text = collectAcpToolText(toolUse)
  const parsedLines = parseCatNContent(text)

  if (!filePath && !parsedLines)
    return null

  return {
    filePath,
    lines: parsedLines,
    totalLines: 0,
    numLines: 0,
    fallbackContent: text,
  }
}
