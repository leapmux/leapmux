// The arithmetic over grep's own output format, shared by the providers whose runtime
// prints that format unchanged.
//
// `path:line:text` is the output contract of grep itself, not the vocabulary of any one
// runtime, so the counting over it belongs to no single plugin. The EMPTY-RESULT wording
// does belong to the runtime: Pi prints `No matches found`, Reasonix prints
// `(no matches)`, and each of the others prints its own sentence. Every one of those
// stays in its own plugin, where a transcript captured from that runtime is the
// evidence for it.

/** What a grep-style output states: the lines that matched, and the files they sit in. */
export interface GrepMatches {
  /** Each output line that states a match. */
  lines: string[]
  /** How many distinct files those lines point at. */
  numFiles: number
}

/** A match line: a path, then the line number, then the matched text. */
const GREP_MATCH_LINE = /^.+:\d+:/

/** The `:<line>:<text>` tail of a match line, which leaves the path behind. */
const GREP_MATCH_TAIL = /:\d+:.*/

/**
 * The matches one grep-style output states.
 *
 * `lines` is the OUTPUT split into lines, because the two callers split it for their own
 * reasons already. A line that states no match -- a heading, a truncation notice, the
 * runtime's own empty sentence -- counts for nothing here, so a caller may pass the whole
 * output.
 */
export function grepMatches(lines: readonly string[]): GrepMatches {
  const matched = lines.filter(line => GREP_MATCH_LINE.test(line))
  return { lines: matched, numFiles: new Set(matched.map(line => line.replace(GREP_MATCH_TAIL, ''))).size }
}
