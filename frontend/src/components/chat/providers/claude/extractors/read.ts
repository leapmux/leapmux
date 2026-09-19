import type { ReadFileResult } from '../../../ir/readFileResult'
import type { ToolCallPayloadForKind } from '../../../ir/toolCall'
import type { ReadRequest } from '../../../ir/tools/read'
import type { ClaudeToolRow } from './toolCommon'
import { pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { parseReadContent, readFileResultFromContent } from '../../../ir/readFileResult'
import { claudeFailedResult } from './failure'

/** Non-text Read variants that the catch-all renderer continues to handle. */
const NON_TEXT_READ_TYPES = new Set(['image', 'notebook', 'pdf', 'parts', 'file_unchanged'])

interface ClaudeReadInputArg {
  toolUseResult?: Record<string, unknown> | null
  resultContent: string
}

/**
 * Build a ReadFileResult from a Claude `Read` tool_result. Returns null
 * for non-text variants (image/notebook/pdf/parts/file_unchanged), letting
 * downstream renderers fall back to their existing handling for those.
 *
 * For text variants the structured `tool_use_result.file` payload is preferred;
 * otherwise the raw `resultContent` is parsed as cat-n format (subagent
 * fallback). When neither parses, `lines` is null and the body renders
 * `resultContent` as plain text.
 */
export function claudeReadFromToolResult(args: ClaudeReadInputArg): ReadFileResult | null {
  const { toolUseResult, resultContent } = args

  const variantType = pickString(toolUseResult, 'type')
  if (variantType && NON_TEXT_READ_TYPES.has(variantType))
    return null

  // The leading/trailing <tag> blocks (system-reminders, etc.) live in the raw
  // resultContent regardless of whether a structured `file` payload is present, so
  // extract them here and attach to whichever source shape we build.
  const { leading, lines, trailing } = parseReadContent(resultContent)
  const file = pickObject(toolUseResult, 'file')

  if (file) {
    const fileContent = pickString(file, 'content')
    const startLine = pickNumber(file, 'startLine', 1)
    return {
      ...readFileResultFromContent({
        content: fileContent,
        startLine,
        fallbackContent: resultContent,
      }),
      leading,
      trailing,
    }
  }

  // Subagent fallback: the raw resultContent's cat-n body, plus its reminders.
  return {
    lines,
    fallbackContent: resultContent,
    leading,
    trailing,
  }
}

/**
 * The read pair: the file it read and the range it asked for. A non-text read
 * states no lines, and the row shows the pictures the result carried instead.
 *
 * A read that FAILED states its reason alone. The file viewer took it as the file's
 * own body otherwise, which reads as a one-line file rather than as an error.
 */
export function claudeReadPayload(request: ReadRequest, result: ClaudeToolRow | undefined): ToolCallPayloadForKind<'read'> {
  if (!result)
    return { kind: 'read', request }
  const failure = claudeFailedResult(result)
  if (failure)
    return { kind: 'read', request, result: failure, images: result.images }
  // A non-text read -- an image, a notebook, a PDF -- states no lines, and the
  // result keeps the text it carried beside the pictures in `images`. The
  // record rides only when the result row carries one.
  const source = claudeReadFromToolResult({
    ...(result.toolUseResult !== undefined ? { toolUseResult: result.toolUseResult } : {}),
    resultContent: result.resultContent,
  }) ?? { lines: null, fallbackContent: result.resultContent }
  return {
    kind: 'read',
    request,
    result: source,
    images: result.images,
  }
}
