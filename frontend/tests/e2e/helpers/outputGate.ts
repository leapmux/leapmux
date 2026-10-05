import { randomUUID } from 'node:crypto'
import { existsSync, statSync, writeFileSync } from 'node:fs'
import { isAbsolute, join } from 'node:path'
import { withCleanup } from './cleanup'
import { quotePosixShellArgument } from './shellArguments'

/**
 * How often a held shell looks for its release file, in seconds.
 *
 * This is a poll interval. No correct result depends on its value, because the
 * release file is the only thing that ends the wait.
 */
const POLL_SECONDS = '0.05'

/** The name of the shell function that waits for the release file. */
const HOLD_FUNCTION = 'leapmux_hold_output'

/**
 * A single-use hold on the shells of shell commands.
 *
 * A native shell tool can lose the output of a command that exits right after it
 * writes. MiMo Code 0.1.15 does so (`BashTool.run`):
 * - Its bash tool reads the output in a fiber.
 * - The scope that runs the command also owns the fiber.
 * - The scope ends when the command exits.
 * - The end interrupts the fiber before it reads the last bytes.
 * - The tool then answers "(no output)".
 *
 * A command that stays alive after it wrote cannot lose its output, because the
 * fiber reads the bytes while the command still runs.
 *
 * A held command stays alive until `release`. A spec calls `release` only after
 * the browser shows the output of the running command. The output reaches the
 * browser from the same fiber, after the fiber kept the bytes. So the release
 * comes after the tool has the output, and the tool cannot lose it.
 */
export interface OutputGate {
  /** The file whose existence releases every command that this gate holds. */
  readonly releasePath: string
  /**
   * Wrap `command` so that its shell stays alive after the command ends, until `release`.
   *
   * The shell keeps the exit status of `command`, and it keeps the output of both
   * streams. The hold is an EXIT trap, so it also holds a command that calls
   * `exit`. A command that sets its own EXIT trap, or that replaces its shell with
   * `exec`, removes the hold.
   */
  hold: (command: string) => string
  /** Let every held command end. A second call changes nothing. */
  release: () => void
}

/**
 * Create a gate whose release file lives in `directory`.
 *
 * Every gate takes its own file name, so two gates can share a directory. The
 * directory must exist and must be an absolute path.
 */
export function createOutputGate(directory: string): OutputGate {
  if (!isAbsolute(directory))
    throw new Error('The output gate requires an absolute directory.')
  if (!existsSync(directory) || !statSync(directory).isDirectory())
    throw new Error('The output gate requires an existing directory.')
  const releasePath = join(directory, `output-gate-release-${randomUUID()}`)
  let released = false
  return {
    releasePath,
    hold: (command) => {
      if (!command.trim())
        throw new Error('The output gate requires a command to hold.')
      // A function holds the wait, so the trap needs no second level of quotation marks around the path.
      return `${HOLD_FUNCTION}() { while [ ! -e ${quotePosixShellArgument(releasePath)} ]; do sleep ${POLL_SECONDS}; done; }; trap ${HOLD_FUNCTION} EXIT; ${command}`
    },
    release: () => {
      if (released)
        return
      writeFileSync(releasePath, '')
      released = true
    },
  }
}

/** A gate, and the observation that the browser shows the output of the command that the gate holds. */
export interface GatedOutput {
  gate: OutputGate
  /** Resolve when the browser shows the output of the running command. Reject when it never does. */
  shown: () => Promise<void>
}

/**
 * Run `run` while a gate holds a command, and release the gate after the browser shows its output.
 *
 * `run` starts at once and runs beside the observation, because it can need an
 * approval click while the output shows. The call releases the gate when the
 * observation ends in a failure, and when `run` fails, so no command stays alive
 * after the call. Without a gate the call only runs `run`.
 */
export async function runWithGatedOutput<T>(gated: GatedOutput | undefined, run: () => Promise<T>): Promise<T> {
  if (!gated)
    return run()
  const { gate, shown } = gated
  return withCleanup(async () => {
    const [result] = await Promise.all([run(), shown().then(() => gate.release())])
    return result
  }, async () => gate.release())
}
