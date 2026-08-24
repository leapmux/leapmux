// Shared primitives for the repo guards that scan source TEXT rather than a
// runtime: `noDuplicateModuleMocks.test.ts` and `noNetworkIdleWait.test.ts`.
//
// Both ban a construct, and both have to EXPLAIN the ban somewhere, so both
// quote the construct in a comment. Each carried a byte-identical copy of the
// comment-line pattern below, so a change to what counts as a comment -- a
// fourth opener, a docblock style -- would have moved one guard and left the
// other reading its own explanation as an offence.
//
// NOT a `.test.ts`, so vitest does not collect this module as a suite of its
// own. Its cases live in `sourceScan.test.ts` beside it.

/**
 * A line whose first non-space characters open or continue a comment.
 *
 * A trailing comment on a line of code is NOT one of these -- the line still
 * carries code, so it still counts, and a note about a banned call belongs
 * above the line rather than after it.
 */
const COMMENT_LINE = /^\s*(?:\/\/|\/\*|\*)/

/**
 * `source` with every comment line replaced by an empty line.
 *
 * Blanking rather than deleting keeps the line count and every code line's
 * number, so a scanner reports a position in the ORIGINAL file. It also leaves
 * the newlines in place, so a pattern that spans lines with `\s*` still
 * matches a call that the formatter wrapped.
 */
export function stripCommentLines(source: string): string {
  return source
    .split('\n')
    .map(line => (COMMENT_LINE.test(line) ? '' : line))
    .join('\n')
}

/** The 1-based number of the line that holds `index` in `source`. */
export function lineNumberAt(source: string, index: number): number {
  let line = 1
  for (let i = 0; i < index && i < source.length; i++) {
    if (source[i] === '\n')
      line++
  }
  return line
}
