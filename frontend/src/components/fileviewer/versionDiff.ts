import type { ComputedDiff } from '~/components/chat/diff/diffCompute'
import type { StructuredPatchHunk, TextsDiffInput } from '~/components/chat/diff/diffTypes'

// ---------------------------------------------------------------------------
// The diff of two file versions that a read limit may cut
//
// The file viewer reads at most a fixed number of bytes of each version. The two
// reads end at the same byte count, but not at the same place in the file: an
// insertion before the cut moves the end of one read up, and a deletion moves it
// down. A diff of the two prefixes would therefore show the lines that only one
// read holds as removed or added, though the file did not change there. This
// module compares only what both reads can answer, and states where the diff
// stops.
// ---------------------------------------------------------------------------

/** The first bytes of one version of a file, and whether the read limit cut them. */
export interface VersionRead {
  bytes: Uint8Array
  truncated: boolean
}

/** The read of a version that does not exist, which compares as an empty file. */
export const ABSENT_VERSION: VersionRead = {
  bytes: new Uint8Array(),
  truncated: false,
}

/** Two versions that a diff compares, as the diff worker receives them. */
export interface ComparedVersions {
  /** The texts that the diff compares (see comparableText). Pass this object to the diff worker on each request. */
  input: TextsDiffInput
  oldTruncated: boolean
  newTruncated: boolean
}

/**
 * The most line edits that the diff of two versions looks for (see DiffInput).
 * A re-indent or a move of up to 2,500 lines therefore draws its minimal diff.
 *
 * The diff worker answers one request at a time, so this limit also caps how
 * long a viewer diff holds back the diffs of the chat rows. Measured with jsdiff
 * 9.0.0 on Node, on a 7,400-line file of 256 KiB (the read limit of the viewer):
 *
 * - A re-indent of 5,000 line edits takes about 1.05 s, against 71 ms for 1,200
 *   edits and about 3.55 s for 10,000.
 * - A full rewrite makes the search give up after about 1.08 s, against 49 ms at
 *   the chat row's limit of 1,000 and 3.85 s at a limit of 10,000.
 */
export const VERSION_DIFF_MAX_EDITS = 5000

/** What a view draws for the diff of two versions. */
export interface ShownVersionDiff {
  /** The diff without the changes that only a cut makes up. */
  diff: ComputedDiff
  /** The old lines that the diff covers, which its gap separators reveal. */
  originalFile: string
  /** Whether the diff stops where a read of one version stopped, before the end of the file. */
  stopsAtCut: boolean
}

const NEWLINE = 0x0A

const utf8 = new TextDecoder()

/**
 * The text of `version` that a diff can compare: the whole text of a version
 * that the limit did not cut, and the complete lines of one that it cut. The
 * bytes after the last newline of a cut version belong to a line that the cut
 * splits, and the cut can also split a UTF-8 character. A newline byte never
 * occurs inside a multi-byte UTF-8 character, so the text up to it decodes whole.
 *
 * A cut version that holds no newline is one line longer than the read. Its
 * comparable text is that line as far as the read goes, without a character that
 * the cut splits: without it, the diff would compare an empty text.
 */
export function comparableText(version: VersionRead): string {
  if (!version.truncated)
    return utf8.decode(version.bytes)
  const end = version.bytes.lastIndexOf(NEWLINE) + 1
  if (end > 0)
    return utf8.decode(version.bytes.subarray(0, end))
  // A streaming decode keeps back the bytes of a character that the input splits
  // at its end. The decoder is discarded, and those bytes with it.
  return new TextDecoder().decode(version.bytes, { stream: true })
}

/**
 * The diff input of two versions, with whether a cut stops each one.
 *
 * The viewer mounts one copy of the diff, so the input draws every line: the line
 * limit of a chat row exists because a chat row mounts two copies. The diff then
 * computes the word diffs of every line too.
 */
export function compareVersions(oldVersion: VersionRead, newVersion: VersionRead): ComparedVersions {
  return {
    input: {
      kind: 'texts',
      oldText: comparableText(oldVersion),
      newText: comparableText(newVersion),
      drawnLineLimit: Number.POSITIVE_INFINITY,
      maxLineEdits: VERSION_DIFF_MAX_EDITS,
    },
    oldTruncated: oldVersion.truncated,
    newTruncated: newVersion.truncated,
  }
}

function isChangedLine(line: string | undefined): boolean {
  return line?.[0] === '-' || line?.[0] === '+'
}

/** `text` without its last `count` lines. Each line of `text` ends with a newline, except perhaps the last. */
function withoutLastLines(text: string, count: number): string {
  let end = text.endsWith('\n')
    ? text.length - 1
    : text.length
  for (let i = 0; i < count; i++) {
    end = text.lastIndexOf('\n', end - 1)
    if (end < 0)
      return ''
  }
  return text.slice(0, end + 1)
}

/**
 * What a view draws for `computed`, the diff of `versions`.
 *
 * Only the change block at the very end of the diff, a run of changed lines with
 * no unchanged line after it, can come from a cut. A removed line of that block
 * may still exist past the cut of the new version, and an added line may still
 * exist past the cut of the old one. The view draws neither, so the diff stops
 * where the viewer stops reading one of the versions. Each earlier change has an
 * unchanged line after it, which aligns the two reads, so it is a real change.
 *
 * The diff keeps a final block that no unchanged line comes before: it holds every
 * line of both reads, as the change of a file of one long line does, and without
 * it the view would draw no diff at all. A whole replacement keeps every line too,
 * because its notice already states that it pairs no line.
 */
export function shownVersionDiff(computed: ComputedDiff, versions: ComparedVersions): ShownVersionDiff {
  const stopsAtCut = versions.oldTruncated || versions.newTruncated
  const whole: ShownVersionDiff = {
    diff: computed,
    originalFile: versions.input.oldText,
    stopsAtCut,
  }
  const last = computed.hunks.at(-1)
  if (!stopsAtCut || computed.wholeReplacement !== null || last === undefined)
    return whole

  let blockStart = last.lines.length
  while (blockStart > 0 && isChangedLine(last.lines[blockStart - 1]))
    blockStart--
  const endsWithChange = blockStart < last.lines.length
  // The first hunk starts at old line 1 only when no unchanged line comes before it.
  const followsUnchangedLine = blockStart > 0 || computed.hunks.length > 1 || last.oldStart > 1
  if (!endsWithChange || !followsUnchangedLine)
    return whole

  const madeUpByCut = (line: string): boolean =>
    (line[0] === '-' && versions.newTruncated) || (line[0] === '+' && versions.oldTruncated)
  const kept = last.lines.filter((line, index) => index < blockStart || !madeUpByCut(line))
  if (kept.length === last.lines.length)
    return whole
  const droppedOldLines = last.lines.slice(blockStart).filter(line => line[0] === '-' && madeUpByCut(line)).length
  const trimmed: StructuredPatchHunk = {
    oldStart: last.oldStart,
    oldLines: kept.filter(line => line[0] !== '+').length,
    newStart: last.newStart,
    newLines: kept.filter(line => line[0] !== '-').length,
    lines: kept,
  }
  const earlier = computed.hunks.slice(0, -1)
  return {
    diff: {
      ...computed,
      hunks: kept.some(isChangedLine)
        ? [...earlier, trimmed]
        : earlier,
    },
    // The removed lines of the final block are the last lines of the old read. The
    // gaps must not reveal them as unchanged lines.
    originalFile: withoutLastLines(versions.input.oldText, droppedOldLines),
    stopsAtCut,
  }
}
