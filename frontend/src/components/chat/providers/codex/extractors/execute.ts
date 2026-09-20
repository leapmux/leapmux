import type { CommandResult } from '../../../model/commandResult'
import { CODEX_ITEM, CODEX_ITEM_FIELD } from '~/generated/contracts/codex-protocol'
import { pickNumber, pickString } from '~/lib/jsonPick'

/** Regex to strip shell wrappers like `/bin/zsh -lc '...'` from commands. */
const SHELL_WRAPPER_RE = /^\/bin\/(?:ba|z)?sh\s+-lc\s+'(.+)'$/

/** Extract command output and status. Return null for other item types. */
export function codexCommandFromItem(item: Record<string, unknown> | null | undefined): CommandResult | null {
  if (!item || item.type !== CODEX_ITEM.CommandExecution)
    return null

  const exitCode = pickNumber(item, 'exitCode')
  return {
    output: pickString(item, CODEX_ITEM_FIELD.AggregatedOutput),
    exitCode,
    durationMs: pickNumber(item, 'durationMs'),
  }
}

/** Strip a shell wrapper like `/bin/zsh -lc '...'` to surface the actual command. */
export function codexUnwrapCommand(rawCommand: string): string {
  return rawCommand.replace(SHELL_WRAPPER_RE, '$1')
}
