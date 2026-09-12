/**
 * The exit-code line a command result can begin with, in the two spellings
 * providers emit:
 *
 * - `Exit code 1` -- Claude Code and ZCode, as the first line of the result text.
 * - `exit code: 1` -- Goose, as a content block of its own ahead of the output.
 *
 * Anchored to the START of the result, because the same words inside a command's
 * own output belong to that command rather than to the provider that ran it.
 *
 * Every following newline is consumed with it. Goose's marker arrives as a
 * separate block, so joining leaves a blank line where it was, and keeping that
 * would put an empty first line above every failed Goose command. A blank line
 * that a command's own output began with is indistinguishable from that one in
 * the joined text, and losing it costs the reader nothing.
 */
const EXIT_CODE_MARKER = /^(?:Exit code|exit code:) (\d+)(?:\n+|$)/

/**
 * Split a leading `Exit code N` line off a command result.
 *
 * Returns the code the line states and the output without it, or the text
 * unchanged and no code when the result does not begin with one.
 *
 * Two providers emit this line and a caller in each one consumes it, so the code
 * reaches the reader ONCE -- in the status label, where every provider puts it.
 * Claude Code states the code here and nowhere else, so without this its rows read
 * `Error` where the others read `Error (exit 1)`. ZCode states it here AND as a
 * structured field, so without this its rows show the code twice.
 *
 * The parsing lives here rather than in either plugin because both consume the
 * same line; the DECISION to consume it stays with each plugin, which is what
 * knows whether its own provider emits it.
 *
 * This is its own module rather than part of `./commandResult.tsx` because a
 * provider extractor is imported by tests that run in the NODE environment. That
 * file holds JSX, and a value import of it from there fails to parse -- the type
 * import those extractors already had is erased and never showed the problem.
 */
export function splitExitCodeMarker(text: string): { output: string, exitCode?: number } {
  const marker = EXIT_CODE_MARKER.exec(text)
  if (!marker)
    return { output: text }
  return { output: text.slice(marker[0].length), exitCode: Number(marker[1]) }
}
