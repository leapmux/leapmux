import type { CommandResultSource } from '../../../results/commandResult'
import { pickNumber, pickString } from '~/lib/jsonPick'

/** Regex to strip shell wrappers like `/bin/zsh -lc '...'` from commands. */
const SHELL_WRAPPER_RE = /^\/bin\/(?:ba|z)?sh\s+-lc\s+'(.+)'$/

/**
 * Build a CommandResultSource from a Codex `commandExecution` item. Reads
 * defensively because `cwd` and `durationMs` are wire-format additions
 * not in the public Rust struct.
 *
 * Returns null when the item type isn't `commandExecution`.
 */
export function codexCommandFromItem(item: Record<string, unknown> | null | undefined): CommandResultSource | null {
  if (!item || item.type !== 'commandExecution')
    return null

  const status = pickString(item, 'status')
  const exitCode = pickNumber(item, 'exitCode')
  const isError = status === 'failed' || (exitCode !== null && exitCode !== 0)

  return {
    output: pickString(item, 'aggregatedOutput'),
    exitCode,
    durationMs: pickNumber(item, 'durationMs'),
    isError,
  }
}

/** Strip a shell wrapper like `/bin/zsh -lc '...'` to surface the actual command. */
export function codexUnwrapCommand(rawCommand: string): string {
  return rawCommand.replace(SHELL_WRAPPER_RE, '$1')
}
