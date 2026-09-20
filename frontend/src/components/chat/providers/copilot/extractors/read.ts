import type { ReadFileResult } from '../../../model/readFileResult'
import { pickString } from '~/lib/jsonPick'
import { parseUnifiedDiff } from '../../../diff'
import { readFileResultFromContent } from '../../../model/readFileResult'
/** Native view details use context-only diff hunks to preserve file positions. */
export function copilotReadResult(raw: Record<string, unknown>, input: Record<string, unknown>): ReadFileResult {
  const filePath = pickString(input, 'path')
  const content = pickString(raw, 'content')
  const details = pickString(raw, 'detailedContent')
  const first = Array.isArray(input.view_range) ? input.view_range[0] : undefined
  const startLine = typeof first === 'number' && Number.isSafeInteger(first) && first > 0 ? first : 1
  const fallback = readFileResultFromContent({ content, startLine })
  const oldPaths = [...details.matchAll(/^--- (.+)$/gm)]
  const newPaths = [...details.matchAll(/^\+\+\+ (.+)$/gm)]
  const path = filePath.replaceAll('\\', '/').replace(/^\/+/, '')
  // Exactly one path on each side, or the details are not a self-contained diff.
  const oldPath = oldPaths.length === 1 ? oldPaths[0]?.[1] : undefined
  const newPath = newPaths.length === 1 ? newPaths[0]?.[1] : undefined
  if (!path || oldPath !== `a/${path}` || newPath !== `b/${path}`) {
    return fallback
  }
  const parsed = parseUnifiedDiff(details)
  if (!parsed)
    return fallback
  let endLine = 0
  const lines: NonNullable<ReadFileResult['lines']> = []
  for (const hunk of parsed.hunks) {
    if (!Number.isSafeInteger(hunk.newStart) || !Number.isSafeInteger(hunk.newLines)
      || hunk.newStart <= endLine || hunk.newLines < 1
      || hunk.oldStart !== hunk.newStart || hunk.oldLines !== hunk.newLines
      || hunk.lines.length !== hunk.newLines || hunk.lines.some(line => !line.startsWith(' '))) {
      return fallback
    }
    endLine = hunk.newStart + hunk.newLines - 1
    if (!Number.isSafeInteger(endLine))
      return fallback
    for (const [index, line] of hunk.lines.entries())
      lines.push({ num: hunk.newStart + index, text: line.slice(1) })
  }
  return { ...fallback, lines, fallbackContent: lines.map(line => line.text).join('\n') }
}
