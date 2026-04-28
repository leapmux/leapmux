import type { FileEditDiffSource } from '../../../results/fileEditDiff'
import type { ReadFileResultSource } from '../../../results/readFileResult'
import { isObject, pickNumber, pickString } from '~/lib/jsonPick'
import { fileEditDiffFromHunks, fileEditDiffFromNewFile } from '../../../results/fileEditDiff'
import { readFileSourceFromContent } from '../../../results/readFileResult'
import { PI_TOOL } from '../protocol'
import { parsePiNumberedDiff } from './piDiffParser'
import { piExtractTool } from './toolCommon'

/**
 * Pi `edit` tool — Pi may apply multiple substitutions in one call, so we
 * surface one `FileEditDiffSource` per substitution. The renderer renders
 * each diff in sequence, mirroring how Pi describes the operation.
 */
export interface PiEditResult {
  path: string
  sources: FileEditDiffSource[]
  isError: boolean
}

/** Extract a Pi edit tool execution. Returns null when not the edit tool. */
export function extractPiEdit(payload: Record<string, unknown> | null | undefined): PiEditResult | null {
  const tool = piExtractTool(payload ?? undefined)
  if (!tool || tool.toolName !== PI_TOOL.Edit)
    return null
  const path = pickString(tool.args, 'path')
  const editsRaw = tool.args.edits
  const sources: FileEditDiffSource[] = []
  if (Array.isArray(editsRaw)) {
    for (const e of editsRaw) {
      if (!isObject(e))
        continue
      sources.push({
        filePath: path,
        structuredPatch: null,
        oldStr: typeof e.oldText === 'string' ? e.oldText : '',
        newStr: typeof e.newText === 'string' ? e.newText : '',
      })
    }
  }
  return { path, sources, isError: tool.isError }
}

/**
 * Extract a Pi write tool execution as an "all-added" diff source. Returns
 * null when not the write tool. We don't have the prior file contents, so
 * the new-file shape (empty old, full content as new) is what the diff view
 * renders — matches how other providers render fresh file writes.
 */
export function extractPiWrite(payload: Record<string, unknown> | null | undefined): FileEditDiffSource | null {
  const tool = piExtractTool(payload ?? undefined)
  if (!tool || tool.toolName !== PI_TOOL.Write)
    return null
  return fileEditDiffFromNewFile(pickString(tool.args, 'path'), pickString(tool.args, 'content'))
}

/**
 * Pi edit/write `tool_execution_end` carries the actually-applied diff in
 * `result.details.diff` (Pi's numbered-line format). Resolve it into a
 * `FileEditDiffSource` against the original `tool_execution_start` args.
 * Returns the parsed source and the raw diff text; the source is null when
 * there is no diff or it fails to parse.
 *
 * Memoized by payload identity: `piToolResultMeta` and the result-body
 * renderer both call this for the same payload per render, so without a
 * cache `parsePiNumberedDiff` runs twice on the same diff text.
 */
const diffCache = new WeakMap<Record<string, unknown>, ResolvedPiResultDiff>()

interface ResolvedPiResultDiff {
  source: FileEditDiffSource | null
  rawDiff: string
}

export function resolvePiResultDiff(
  payload: Record<string, unknown>,
  startArgs: Record<string, unknown>,
): ResolvedPiResultDiff {
  const cached = diffCache.get(payload)
  if (cached)
    return cached
  const tool = piExtractTool(payload)
  const rawDiff = pickString(tool?.result?.details, 'diff')
  let resolved: ResolvedPiResultDiff
  if (!rawDiff) {
    resolved = { source: null, rawDiff }
  }
  else {
    const hunks = parsePiNumberedDiff(rawDiff)
    resolved = (!hunks || hunks.length === 0)
      ? { source: null, rawDiff }
      : { rawDiff, source: fileEditDiffFromHunks(pickString(startArgs, 'path'), hunks) }
  }
  diffCache.set(payload, resolved)
  return resolved
}

/**
 * Pi `read` tool result + the original args (offset/limit) so the renderer
 * can show the requested range in the title.
 */
export interface PiReadResult {
  source: ReadFileResultSource
  offset: number | null
  limit: number | null
}

/** Extract a Pi read tool execution. Returns null when not the read tool. */
export function extractPiRead(
  payload: Record<string, unknown> | null | undefined,
  fallbackArgs?: Record<string, unknown>,
): PiReadResult | null {
  const tool = piExtractTool(payload ?? undefined)
  if (!tool || tool.toolName !== PI_TOOL.Read)
    return null

  // `tool_execution_end` carries the result but not the original args; callers
  // pass the matching start args via `fallbackArgs` so the shared Read body can
  // still show the correct path and line numbers.
  const args = Object.keys(tool.args).length > 0 ? tool.args : (fallbackArgs ?? {})
  const resultText = tool.result?.text ?? tool.partialResult?.text ?? ''
  return {
    source: readFileSourceFromContent({
      filePath: pickString(args, 'path'),
      content: resultText,
      startLine: pickNumber(args, 'offset', 1),
    }),
    offset: pickNumber(args, 'offset'),
    limit: pickNumber(args, 'limit'),
  }
}
