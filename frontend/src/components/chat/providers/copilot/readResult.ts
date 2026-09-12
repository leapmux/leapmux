import type { ReadFileResultSource } from '../../results/readFileResult'
import { pickString } from '~/lib/jsonPick'
import { parseUnifiedDiff } from '../../diff'
import { readFileSourceFromContent } from '../../results/readFileResult'

/** Native view details use context-only diff hunks to preserve file positions. */
export function copilotReadResult(raw: Record<string, unknown>, input: Record<string, unknown>): ReadFileResultSource {
  const filePath = pickString(input, 'path')
  const content = pickString(raw, 'content')
  const details = pickString(raw, 'detailedContent')
  const first = Array.isArray(input.view_range) ? input.view_range[0] : undefined
  const startLine = typeof first === 'number' && Number.isSafeInteger(first) && first > 0 ? first : 1
  const fallback = readFileSourceFromContent({ filePath, content, startLine })
  const oldPaths = [...details.matchAll(/^--- (.+)$/gm)]
  const newPaths = [...details.matchAll(/^\+\+\+ (.+)$/gm)]
  const path = filePath.replaceAll('\\', '/').replace(/^\/+/, '')
  if (!path || oldPaths.length !== 1 || newPaths.length !== 1
    || oldPaths[0][1] !== `a/${path}` || newPaths[0][1] !== `b/${path}`) {
    return fallback
  }
  const parsed = parseUnifiedDiff(details)
  if (!parsed)
    return fallback
  let endLine = 0
  const lines: NonNullable<ReadFileResultSource['lines']> = []
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
  return { ...fallback, lines, numLines: lines.length, fallbackContent: lines.map(line => line.text).join('\n') }
}
