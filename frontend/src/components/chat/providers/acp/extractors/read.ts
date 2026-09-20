import type { ReadFileResult } from '../../../model/readFileResult'
import { ACP_SUPPLEMENT_REQUEST } from '~/generated/contracts/acp-protocol'
import { pickFirstString, pickObject } from '~/lib/jsonPick'
import { parseReadContent } from '../../../model/readFileResult'
import { TOOL_FILE_PATH_KEYS } from '../../toolInputKeys'
import { collectAcpToolText } from '../content'

/**
 * Build a ReadFileResult from an ACP `tool_call_update` of kind `read`.
 * Returns null when neither a filePath nor parseable cat-n output is present —
 * letting the caller fall back to the generic text branch.
 *
 * When the raw output parses as cat-n format, `lines` is populated so the
 * shared body renders the syntax-highlighted view. Otherwise `lines` is null
 * and the body shows the raw text via `fallbackContent`.
 */
export function acpReadFromToolCall(toolUse: Record<string, unknown> | null | undefined): ReadFileResult | null {
  if (!toolUse)
    return null

  const rawInput = pickObject(toolUse, ACP_SUPPLEMENT_REQUEST.RawInput)
  const filePath = pickFirstString(rawInput, TOOL_FILE_PATH_KEYS) ?? ''

  const text = collectAcpToolText(toolUse, { rawObjects: false })
  const { leading, lines, trailing } = parseReadContent(text)

  if (!filePath && !lines)
    return null

  return {
    lines,
    fallbackContent: text,
    leading,
    trailing,
  }
}
