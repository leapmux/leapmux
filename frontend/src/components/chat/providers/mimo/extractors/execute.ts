import type { CommandResult } from '../../../model/commandResult'
import type { ExecuteRequest, ExecuteResult } from '../../../model/tools/execute'
import type { MiMoToolPart } from './toolCommon'
import { MIMO_TOOL } from '~/generated/contracts/mimo-protocol'
import { pickBoolean, pickNumber, pickString } from '~/lib/jsonPick'
import { MIMO_EXEC_STATUS } from '../protocol'

/**
 * The command one MiMo execution call asked for.
 *
 * `bash` states a shell command and the directory it runs in. `exec` states a
 * JavaScript body that calls MiMo's own tools, so the row draws it as code in that
 * language.
 */
export function mimoExecuteRequest(tool: string, input: Record<string, unknown>): ExecuteRequest {
  const description = pickString(input, 'description')
  if (tool === MIMO_TOOL.Exec)
    return { command: pickString(input, 'code'), language: 'javascript', ...(description ? { description } : {}) }
  const cwd = pickString(input, 'workdir')
  return {
    command: pickString(input, 'command'),
    ...(description ? { description } : {}),
    ...(cwd ? { cwd } : {}),
  }
}

/**
 * The output one finished MiMo execution call printed.
 *
 * A shell command states its whole output in `metadata.output` and its exit code in
 * `metadata.exit`. The `output` field holds the same text, possibly followed by a note
 * that MiMo wrote for the model, so the metadata is read first. A script states its
 * answer in `output` alone: MiMo's `<exec>` wrapper around the return value or the
 * error, and the script's logs.
 */
export function mimoExecuteResult(part: MiMoToolPart): ExecuteResult {
  const metadataOutput = pickString(part.metadata, 'output', undefined)
  const output = metadataOutput ?? part.output
  const exit = pickNumber(part.metadata, 'exit', undefined)
  const truncated = pickBoolean(part.metadata, 'truncated') === true
  const command: CommandResult = {
    output,
    ...(exit !== undefined && Number.isSafeInteger(exit) ? { exitCode: exit } : {}),
    ...(truncated ? { truncated } : {}),
  }
  return { commands: [command], unresolvedTerminals: [] }
}

/**
 * How a finished `exec` script ended, when it did not end well: `failed` or
 * `cancelled`, else null.
 *
 * MiMo completes the call however the script ended, and states the ending in
 * `metadata.status` alone: `completed`, `cancelled` for an abort, and `code_error`,
 * `timeout` or `budget_exceeded` for a script that failed. A word that this build
 * does not know claims no failure that MiMo did not state. A shell command states
 * its ending in its exit code instead, so it has no such word.
 */
export function mimoExecOutcome(part: MiMoToolPart): 'failed' | 'cancelled' | null {
  if (part.tool !== MIMO_TOOL.Exec)
    return null
  switch (pickString(part.metadata, 'status')) {
    case MIMO_EXEC_STATUS.Cancelled:
      return 'cancelled'
    case MIMO_EXEC_STATUS.CodeError:
    case MIMO_EXEC_STATUS.Timeout:
    case MIMO_EXEC_STATUS.BudgetExceeded:
      return 'failed'
    default:
      return null
  }
}
