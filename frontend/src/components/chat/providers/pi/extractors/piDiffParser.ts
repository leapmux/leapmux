import type { StructuredPatchHunk } from '../../../diff'

/**
 * Parse Pi's custom annotated diff format (from `result.details.diff` on
 * the edit tool) into the shared `StructuredPatchHunk[]` shape.
 *
 * Pi's format is line-oriented and embeds line numbers in each line; it is
 * NOT standard unified diff (no `@@ -N,N +N,N @@` headers). Per
 * `pi-mono/packages/coding-agent/src/core/tools/edit-diff.ts:266` each line is:
 *
 *   `+<padded newLineNum> <content>`     added
 *   `-<padded oldLineNum> <content>`     removed
 *   ` <padded oldLineNum> <content>`     context
 *   ` <padded spaces> ...`               skip marker (collapsed context)
 *
 * Skip markers separate hunks. Pi's renderer advances both old and new
 * positions in lockstep across a skip, so we can derive the post-skip new
 * position from the post-skip old position (or vice versa) by reading the
 * line number off the next line.
 *
 * Returns null when any non-empty line fails to match the expected shape
 * — that signals "Pi changed its format", and callers should fall back to
 * rendering the raw `details.diff` text rather than guess. Silently
 * dropping unrecognized lines would corrupt the line-number arithmetic
 * for everything that follows.
 */

const SKIP_MARKER = /^[+\- ]\s*\.\.\.\s*$/
const LINE_RE = /^([+\- ])\s*(\d+) (.*)$/s

interface ParsedLine {
  prefix: '+' | '-' | ' '
  lineNum: number
  content: string
}

function parsePiDiffLine(raw: string): ParsedLine | null {
  const m = LINE_RE.exec(raw)
  if (!m)
    return null
  const lineNum = Number.parseInt(m[2], 10)
  if (!Number.isFinite(lineNum))
    return null
  return { prefix: m[1] as '+' | '-' | ' ', lineNum, content: m[3] }
}

interface HunkAccum {
  oldStart: number
  newStart: number
  oldLines: number
  newLines: number
  lines: string[]
}

function flush(cur: HunkAccum, out: StructuredPatchHunk[]): void {
  if (cur.lines.length === 0)
    return
  out.push({
    oldStart: cur.oldStart,
    oldLines: cur.oldLines,
    newStart: cur.newStart,
    newLines: cur.newLines,
    lines: cur.lines,
  })
}

export function parsePiNumberedDiff(diff: string): StructuredPatchHunk[] | null {
  if (!diff.trim())
    return []
  const out: StructuredPatchHunk[] = []
  // Running positions across the whole diff. Both axes advance in lockstep
  // through skip markers and context lines; +/- advance only their side.
  let oldPos = 1
  let newPos = 1
  // True at the start and after every skip marker — the next line's
  // embedded number tells us where to jump to.
  let pendingAnchor = true
  let cur: HunkAccum | null = null

  for (const raw of diff.split('\n')) {
    if (raw === '')
      continue
    if (SKIP_MARKER.test(raw)) {
      if (cur) {
        flush(cur, out)
        cur = null
      }
      pendingAnchor = true
      continue
    }
    const parsed = parsePiDiffLine(raw)
    if (!parsed) {
      // Refuse to guess past a line we don't understand — line-number
      // arithmetic from this point onward would be unreliable.
      return null
    }

    if (pendingAnchor) {
      if (parsed.prefix === '+') {
        // `+` carries a new-file line number; skip count is on that axis,
        // and old advances by the same delta (Pi advances both in lockstep
        // across context skips).
        const delta = parsed.lineNum - newPos
        oldPos += delta
        newPos = parsed.lineNum
      }
      else {
        // ' ' and '-' carry an old-file line number.
        const delta = parsed.lineNum - oldPos
        newPos += delta
        oldPos = parsed.lineNum
      }
      pendingAnchor = false
    }

    if (!cur)
      cur = { oldStart: oldPos, newStart: newPos, oldLines: 0, newLines: 0, lines: [] }

    if (parsed.prefix === '+') {
      cur.lines.push(`+${parsed.content}`)
      cur.newLines++
      newPos++
    }
    else if (parsed.prefix === '-') {
      cur.lines.push(`-${parsed.content}`)
      cur.oldLines++
      oldPos++
    }
    else {
      cur.lines.push(` ${parsed.content}`)
      cur.oldLines++
      cur.newLines++
      oldPos++
      newPos++
    }
  }

  if (cur)
    flush(cur, out)
  return out
}
