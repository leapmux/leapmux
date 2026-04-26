import type { StructuredPatchHunk } from './diffTypes'

const UNIFIED_DIFF_HEADER_RE = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/

export interface ParsedUnifiedDiff {
  hunks: StructuredPatchHunk[]
  oldText: string
  newText: string
}

/**
 * Parse a header-less unified-diff string into hunks plus reconstructed
 * old/new text. "Header-less" means the input is just hunk blocks
 * (`@@ -N,N +N,N @@` followed by `+`/`-`/` ` lines), no `--- a/...` /
 * `+++ b/...` filename headers — the format Codex's `fileChange` items emit
 * and the format the ACP `tool_call_update.content[].type === 'diff'` shape
 * also carries.
 *
 * Returns null when the input has no hunks. Note: the diff stack's
 * `rawDiffToHunks` is the inverse direction (pre/post text → hunks), so
 * this and `rawDiffToHunks` complement each other.
 */
export function parseUnifiedDiff(diff: string): ParsedUnifiedDiff | null {
  if (!diff.trim())
    return null

  const lines = diff.split('\n')
  const hunks: StructuredPatchHunk[] = []
  const oldLines: string[] = []
  const newLines: string[] = []
  let current: StructuredPatchHunk | null = null

  for (const line of lines) {
    const header = line.match(UNIFIED_DIFF_HEADER_RE)
    if (header) {
      current = {
        oldStart: Number.parseInt(header[1], 10),
        oldLines: header[2] ? Number.parseInt(header[2], 10) : 1,
        newStart: Number.parseInt(header[3], 10),
        newLines: header[4] ? Number.parseInt(header[4], 10) : 1,
        lines: [],
      }
      hunks.push(current)
      continue
    }
    if (!current)
      continue
    if (line.startsWith('\\ No newline at end of file'))
      continue
    if (!line.startsWith('+') && !line.startsWith('-') && !line.startsWith(' '))
      continue

    current.lines.push(line)
    const prefix = line[0]
    const text = line.slice(1)
    if (prefix === '+' || prefix === ' ')
      newLines.push(text)
    if (prefix === '-' || prefix === ' ')
      oldLines.push(text)
  }

  if (hunks.length === 0)
    return null

  return {
    hunks,
    oldText: oldLines.join('\n'),
    newText: newLines.join('\n'),
  }
}

// Streaming renderers re-parse the same diff string on each chunk. Cache by
// diff string content (not by source-object reference — chat.store replaces
// message objects on each update, so references aren't stable). LRU-bounded.
const PARSED_DIFF_CACHE = new Map<string, ParsedUnifiedDiff | null>()
const PARSED_DIFF_CACHE_LIMIT = 64

export function parseUnifiedDiffCached(diff: string): ParsedUnifiedDiff | null {
  if (!diff)
    return null
  const cached = PARSED_DIFF_CACHE.get(diff)
  if (cached !== undefined) {
    // LRU touch: re-insert moves the key to the end of insertion order.
    PARSED_DIFF_CACHE.delete(diff)
    PARSED_DIFF_CACHE.set(diff, cached)
    return cached
  }
  if (PARSED_DIFF_CACHE.size >= PARSED_DIFF_CACHE_LIMIT) {
    const oldestKey = PARSED_DIFF_CACHE.keys().next().value
    if (oldestKey !== undefined)
      PARSED_DIFF_CACHE.delete(oldestKey)
  }
  const parsed = parseUnifiedDiff(diff)
  PARSED_DIFF_CACHE.set(diff, parsed)
  return parsed
}

/** Test-only handle for inspecting the cache state. Imported only by tests. */
export const __unifiedDiffCacheForTest = {
  cache: PARSED_DIFF_CACHE,
  limit: PARSED_DIFF_CACHE_LIMIT,
  parse: parseUnifiedDiffCached,
}
