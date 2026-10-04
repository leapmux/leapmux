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
 * How the COMMAND ended: with a code of its own, with the signal that ended it, or
 * with a failure that its provider states without either -- a command that did not
 * start, or that ran out of time. Never two, and a command that has not returned
 * states none.
 *
 * The code and the signal are exclusive at the source -- one `os.ProcessState`
 * answers with a code or with a signal -- and the label reads the code FIRST. A result
 * that stated both drew "Success" for a process the OS had killed, and dropped the
 * signal on the way. Writing it as a union is what makes that pair impossible to
 * build.
 *
 * `failed` belongs here and not to the row's status, because one call can run
 * several commands: a call that completed can hold one command that failed, and only
 * that command's result can say so.
 */
export type CommandExit
  = | { exitCode?: number | null, signal?: never, failed?: never }
    | { exitCode?: never, signal: string, failed?: never }
    | { exitCode?: never, signal?: never, failed: true }

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
  if (source.failed === true)
    return { failed: true }
  return source.exitCode === null ? { exitCode: null } : {}
}

/**
 * Whether the COMMAND reported a failure: a known non-zero exit code, or -- where no
 * code is known -- the signal that ended it, or a failure with neither.
 *
 * The CALL's own status is a separate question, and the caller asks it separately.
 * This took a `status` parameter once, typed as a bare `string` a caller could spell
 * `'Failed'` into; the one caller passed `undefined` and tested the status itself.
 */
export function commandIsError(exit: CommandExit): boolean {
  if (typeof exit.exitCode === 'number')
    return exit.exitCode !== 0
  return !!exit.signal || exit.failed === true
}

/**
 * One command result with the way it ended REPLACED, never merged: a code, a signal
 * and a failure with neither exclude each other, and the result may already state
 * one of them.
 */
export function withCommandExit(command: CommandResult, exit: CommandExit): CommandResult {
  const { exitCode: _exitCode, signal: _signal, failed: _failed, ...rest } = command
  return { ...rest, ...exit }
}
