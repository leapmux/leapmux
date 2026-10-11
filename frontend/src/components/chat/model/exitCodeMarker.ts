/**
 * Providers use two spellings for a leading command exit line:
 *
 * - `Exit code 1` -- Claude Code, ZCode and Qoder CLI, as the first line of the
 *   result text.
 * - `exit code: 1` -- Goose, as a content block of its own ahead of the output.
 *
 * The parser requires the start of the result.
 * The same words inside command output belong to the command.
 *
 * The parser consumes every line break after the marker.
 * Goose supplies its marker as a separate content block.
 * Joining the blocks leaves a blank line where the marker was.
 * The parser cannot distinguish that line from a blank line printed by the command.
 */
const EXIT_CODE_MARKER = /^(?:Exit code|exit code:) (\d+)(?:\n+|$)/

/**
 * Split a leading `Exit code N` line off a command result.
 *
 * Return a safe integer exit code and the output without the marker.
 * Keep the original text when the marker is absent or its code is invalid.
 *
 * Each provider decides whether its native format supplies this marker.
 * The shared parser keeps the exit code in the status label alone.
 * Claude Code states the code only in this line.
 * ZCode and Qoder CLI also supply a structured exit field.
 *
 * This pure module supports provider extractors and tests in the Node environment.
 * It imports no renderer and draws no markup.
 */
export function splitExitCodeMarker(text: string): { output: string, exitCode?: number } {
  const marker = EXIT_CODE_MARKER.exec(text)
  const exitCode = marker ? Number(marker[1]) : Number.NaN
  if (!marker || !Number.isSafeInteger(exitCode))
    return { output: text }
  return { output: text.slice(marker[0].length), exitCode }
}
