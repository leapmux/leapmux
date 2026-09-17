import type { StructuredPatchHunk } from '../../../diff'
import type { FileEditDiff } from '../../../ir/fileEditDiff'
import type { ReadFileResult } from '../../../ir/readFileResult'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { PI_TOOL } from '~/generated/contracts/pi-protocol'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { parseUnifiedDiffCached } from '../../../diff'
import { fileEditDiffFromHunks, fileEditDiffFromWholeFile, fileEditHasDiff } from '../../../ir/fileEditDiff'
import { readFileResultFromContent } from '../../../ir/readFileResult'
import { parsePiNumberedDiff } from './piDiffParser'
import { piExtractTool, piPairedRequest } from './toolCommon'

/** Match Pi's argument normalization without changing the provider message. */
export function piEditsFromArgs(args: Record<string, unknown>): Array<{ oldText: string, newText: string }> {
  let edits: unknown = args.edits
  if (typeof edits === 'string') {
    try {
      edits = JSON.parse(edits)
    }
    catch {
      edits = undefined
    }
  }
  const values = Array.isArray(edits) ? [...edits] : isObject(edits) ? [edits] : []
  if (typeof args.oldText === 'string' && typeof args.newText === 'string')
    values.push({ oldText: args.oldText, newText: args.newText })
  return values.flatMap(value => isObject(value) && typeof value.oldText === 'string' && typeof value.newText === 'string'
    ? [{ oldText: value.oldText, newText: value.newText }]
    : [])
}

/**
 * The diff sources of one Pi `edit` call. Null when the payload states another tool.
 *
 * Pi may apply several substitutions in one call, so this states ONE
 * `FileEditDiff` per substitution, in the order Pi describes them. The path
 * and the error belong to the call's request and status, which the row already holds.
 */
export function extractPiEdit(payload: Record<string, unknown> | null | undefined): FileEditDiff[] | null {
  const tool = piExtractTool(payload ?? undefined)
  if (!tool || tool.toolName !== PI_TOOL.Edit)
    return null
  const filePath = pickString(tool.args, 'path')
  return piEditsFromArgs(tool.args).map(edit => ({
    filePath,
    structuredPatch: null,
    oldStr: edit.oldText,
    newStr: edit.newText,
  }))
}

/**
 * Extract a Pi write tool execution as an "all-added" diff source. Returns
 * null when not the write tool. We don't have the prior file contents, so
 * the new-file shape (empty old, full content as new) is what the diff view
 * renders — matches how other providers render fresh file writes.
 */
export function extractPiWrite(payload: Record<string, unknown> | null | undefined): FileEditDiff | null {
  const tool = piExtractTool(payload ?? undefined)
  if (!tool || tool.toolName !== PI_TOOL.Write)
    return null
  return fileEditDiffFromWholeFile(pickString(tool.args, 'path'), pickString(tool.args, 'content'), 'add')
}

/**
 * Pi edit/write `tool_execution_end` carries the actually-applied diff in
 * `result.details.diff` (Pi's numbered-line format). Resolve it into a
 * `FileEditDiff` against the original `tool_execution_start` args.
 * Returns the parsed source and the raw diff text; the source is null when
 * there is no diff or it fails to parse.
 *
 * Memoized by payload identity: the row build reads the same payload through
 * `piResolveDiffSources` and again for the raw text an unparseable diff states,
 * so without a cache `parsePiNumberedDiff` runs twice on the same diff text.
 */
const diffCache = new WeakMap<Record<string, unknown>, { hunks: StructuredPatchHunk[] | null, rawDiff: string }>()

interface ResolvedPiResultDiff {
  source: FileEditDiff | null
  rawDiff: string
}

export function resolvePiResultDiff(
  payload: Record<string, unknown>,
  startArgs: Record<string, unknown>,
): ResolvedPiResultDiff {
  let cached = diffCache.get(payload)
  if (!cached) {
    const details = piExtractTool(payload)?.result?.details
    const patch = pickString(details, 'patch')
    const diff = pickString(details, 'diff')
    const hunks = parseUnifiedDiffCached(patch)?.hunks ?? parsePiNumberedDiff(diff)
    cached = { hunks, rawDiff: patch || diff }
    diffCache.set(payload, cached)
  }
  // The request can arrive after the result. Keep its path outside the parsed-diff cache.
  return {
    rawDiff: cached.rawDiff,
    source: cached.hunks?.length ? fileEditDiffFromHunks(pickString(startArgs, 'path'), cached.hunks) : null,
  }
}

/**
 * The file one Pi `read` call returned. Null when the payload states another tool.
 *
 * The requested range is NOT here: the call's request states the offset and the
 * limit, and this reads the offset only to number the lines it returns.
 */
export function extractPiRead(
  payload: Record<string, unknown> | null | undefined,
  fallbackArgs?: Record<string, unknown>,
): ReadFileResult | null {
  const tool = piExtractTool(payload ?? undefined)
  if (!tool || tool.toolName !== PI_TOOL.Read)
    return null

  // `tool_execution_end` carries the result but not the original args; callers
  // pass the matching start args via `fallbackArgs` so the shared Read body can
  // still show the correct path and line numbers.
  const args = Object.keys(tool.args).length > 0 ? tool.args : (fallbackArgs ?? {})
  const resultText = tool.result?.text ?? tool.partialResult?.text ?? ''
  return readFileResultFromContent({
    content: resultText,
    startLine: pickNumber(args, 'offset', 1),
  })
}

/**
 * Fallback diff source(s) for a Pi edit/write whose `tool_execution_end` result
 * carried no `details.diff`: synthesize from the `tool_execution_start` frame
 * (the original edit substitutions / write body). Returns only sources with a
 * renderable diff.
 *
 * The FRAME, not the parsed message that holds it. A caller with no opening event
 * states the row's own frame, and the caller that fabricated a `ParsedMessageContent`
 * around it filled five more fields that nothing here reads.
 */
export function piFallbackDiffSources(
  toolName: string,
  startPayload: Record<string, unknown> | null | undefined,
): FileEditDiff[] {
  if (!isObject(startPayload))
    return []
  if (toolName === PI_TOOL.Edit)
    return extractPiEdit(startPayload)?.filter(fileEditHasDiff) ?? []
  if (toolName === PI_TOOL.Write) {
    const source = extractPiWrite(startPayload)
    return fileEditHasDiff(source) ? [source] : []
  }
  return []
}

/**
 * The diff source(s) a Pi edit/write `tool_execution_end` row renders. They land
 * in the `edit`/`write` result, which the body and the toolbar both read, so the
 * two cannot format the same diff differently. Prefers the inline result diff
 * (`resolvePiResultDiff`), else the tool_use-start fallback. Returns [] for a
 * non-edit/write tool, a failed execution (renders error text, not a diff), or
 * when no diff is present.
 *
 * A PRESENT-but-unparseable result diff (`source` null, `rawDiff` non-empty) does
 * NOT fall through to the start-args fallback: the renderer (PiDiffToolResult)
 * draws the raw diff text as a single `<pre>` block in that case, NOT a structured
 * diff, so synthesizing one here would make `hasDiff` meta model a multi-line diff
 * the body never renders. Mirror the renderer's
 * `isError() || resultDiff().rawDiff` guard.
 */
export function piResolveDiffSources(
  parsed: Record<string, unknown> | null | undefined,
  toolUseParsed: ParsedMessageContent | undefined,
): FileEditDiff[] {
  if (!isObject(parsed))
    return []
  const tool = piExtractTool(parsed)
  if (!tool || (tool.toolName !== PI_TOOL.Edit && tool.toolName !== PI_TOOL.Write))
    return []
  if (tool.isError)
    return []
  const request = piPairedRequest(parsed, toolUseParsed)
  const startArgs = pickObject(request?.parentObject, 'args') ?? {}
  const resolved = resolvePiResultDiff(parsed, startArgs)
  if (resolved.source)
    return [resolved.source]
  // Present-but-unparseable diff: the renderer shows raw text, not a diff body.
  if (resolved.rawDiff)
    return []
  return piFallbackDiffSources(tool.toolName, request?.parentObject)
}
