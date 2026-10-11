import type { ExecuteRequest } from '../../../model/tools/execute'
import { pickString, stringArray } from '~/lib/jsonPick'
import { OH_MY_PI_HUB_OP } from '../protocol'

/**
 * The `hub` operations that supervise a project process: start one, list them, read
 * its output, stop it, restart it, state its launch spec (`tools/hub/index.ts`).
 */
const PROCESS_OPS = new Set<string>([
  OH_MY_PI_HUB_OP.Start,
  OH_MY_PI_HUB_OP.Ps,
  OH_MY_PI_HUB_OP.Logs,
  OH_MY_PI_HUB_OP.Stop,
  OH_MY_PI_HUB_OP.Restart,
  OH_MY_PI_HUB_OP.Describe,
])

/**
 * Whether one `hub` call supervises a process, rather than messages an agent or
 * controls a background job.
 *
 * `send` and `wait` serve both sides: with a process `name`, `send` writes to the
 * process and `wait` waits on it. omp refuses a `send` that states both a process
 * `name` and a recipient `to`, and reads it as a message, as its own approval rule
 * does (`hubApproval`).
 */
export function ohMyPiHubIsProcessOp(args: Record<string, unknown>): boolean {
  const op = pickString(args, 'op')
  if (PROCESS_OPS.has(op))
    return true
  if (!pickString(args, 'name').trim())
    return false
  if (op === OH_MY_PI_HUB_OP.Wait)
    return true
  return op === OH_MY_PI_HUB_OP.Send && !pickString(args, 'to').trim()
}

/** A word that a POSIX shell reads as one word unchanged: the word itself when it holds no special character, else in single quotes. */
function shellWord(word: string): string {
  return /^[\w@%+=:,./-]+$/.test(word) ? word : `'${word.replaceAll('\'', '\'\\\'\'')}'`
}

/**
 * The command one process operation of the `hub` runs, as the command card states it.
 *
 * - `start` runs `application` with `args` directly, with no shell, so the card states
 *   them as the words a shell would need to run the same command.
 * - `send` writes its text, its keys or its signal to the process.
 * - Every other operation states itself and the process it acts on.
 */
export function ohMyPiHubProcessRequest(args: Record<string, unknown>): ExecuteRequest {
  const op = pickString(args, 'op')
  const name = pickString(args, 'name').trim()
  if (op === OH_MY_PI_HUB_OP.Start) {
    const cwd = pickString(args, 'cwd')
    const words = [pickString(args, 'application'), ...stringArray(args.args)]
    return {
      command: words.filter((word, index) => index > 0 || word !== '').map(shellWord).join(' '),
      description: name ? `Start the process ${name}` : 'Start a process',
      ...(cwd ? { cwd } : {}),
    }
  }
  if (op === OH_MY_PI_HUB_OP.Send) {
    const input = [pickString(args, 'text'), ...stringArray(args.keys), pickString(args, 'signal')].filter(Boolean)
    return { command: input.join(' '), description: `Send to the process ${name}` }
  }
  return { command: [op, name].filter(Boolean).join(' ') }
}
