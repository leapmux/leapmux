/**
 * The result of ONE command an execute call ran (Claude `Bash`, Codex
 * `commandExecution`, ACP `execute`). A call that ran several holds one of
 * these for each.
 *
 * `output` is the raw stream (may contain ANSI). Claude's structured Bash
 * payload separates `stdout` and `stderr`; the extractor joins them into
 * `output`, because the body draws one stream.
 *
 * The OUTCOME lives on the row's one status word, not here: a result that
 * also stated `interrupted` or `isError` restated what `status` already
 * says, and the two spellings drifted.
 */
export type CommandResult = CommandResultBase & CommandExit

/**
 * How the PROCESS itself ended: with a code of its own, or with the signal that ended
 * it. Never both, and a command that has not returned states neither.
 *
 * The two are exclusive at the source -- one `os.ProcessState` answers with a code or
 * with a signal -- and the label reads the code FIRST. A result that stated both drew
 * "Success" for a process the OS had killed, and dropped the signal on the way.
 * Writing it as a union is what makes that pair impossible to build.
 */
export type CommandExit
  = | { exitCode?: number | null, signal?: never }
    | { exitCode?: never, signal: string }

interface CommandResultBase {
  output: string
  /** A referenced output stream could not be recovered. This is different from an empty stream. */
  outputUnavailable?: boolean
  durationMs?: number | null
  /** The provider retained only a suffix of the command output. */
  truncated?: boolean
  /** What identifies this command inside a call that ran several. */
  label?: string
}

/**
 * How ONE command ended, as the row words it.
 *
 * Takes the whole result rather than two fields, so a caller cannot hand the label a
 * code and a signal that the source itself could never carry together.
 */
export function commandExit(source: CommandResult): CommandExit {
  // The CODE answers first, the same order `commandStatusLabel` reads them in. The
  // type forbids the pair, but a payload built through a cast can still carry it,
  // and the two must then word the row the same way. A `null` code is a code the
  // process reported as none; a key that never existed stays absent.
  if (source.exitCode !== undefined && source.exitCode !== null)
    return { exitCode: source.exitCode }
  if (source.signal !== undefined)
    return { signal: source.signal }
  return source.exitCode === null ? { exitCode: null } : {}
}

/**
 * Whether the PROCESS reported a failure: a known non-zero exit code, or -- where no
 * code is known -- the signal that ended it.
 *
 * The CALL's own status is a separate question, and the caller asks it separately.
 * This took a `status` parameter once, typed as a bare `string` a caller could spell
 * `'Failed'` into; the one caller passed `undefined` and tested the status itself.
 */
export function commandIsError(exit: CommandExit): boolean {
  if (typeof exit.exitCode === 'number')
    return exit.exitCode !== 0
  return !!exit.signal
}
