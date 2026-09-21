import type { CommandResult } from '../../../model/commandResult'
import type { CommandAction } from '../../../model/tools/execute'
import { CODEX_ITEM, CODEX_ITEM_FIELD } from '~/generated/contracts/codex-protocol'
import { isObject, pickNumber, pickString } from '~/lib/jsonPick'

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

/** Translate Codex's best-effort command breakdown into the shared execute model. */
export function codexCommandActionsFromItem(item: Record<string, unknown>): CommandAction[] {
  const rawActions: unknown[] = Array.isArray(item.commandActions) ? item.commandActions : []
  const actions: CommandAction[] = []

  for (const value of rawActions) {
    if (!isObject(value))
      continue
    const command = pickString(value, 'command')
    if (!command)
      continue

    switch (pickString(value, 'type')) {
      case 'read': {
        const name = pickString(value, 'name')
        const path = pickString(value, 'path')
        actions.push(name && path
          ? { kind: 'read', command, name, path }
          : { kind: 'unknown', command })
        break
      }
      case 'listFiles': {
        const path = pickString(value, 'path') || undefined
        actions.push({ kind: 'list', command, ...(path !== undefined ? { path } : {}) })
        break
      }
      case 'search': {
        const query = pickString(value, 'query') || undefined
        const path = pickString(value, 'path') || undefined
        actions.push({
          kind: 'search',
          command,
          ...(query !== undefined ? { query } : {}),
          ...(path !== undefined ? { path } : {}),
        })
        break
      }
      default:
        // Preserve explicit `unknown` actions and future Codex variants. The raw
        // command remains useful even when this client cannot describe the action.
        actions.push({ kind: 'unknown', command })
    }
  }

  return actions
}
