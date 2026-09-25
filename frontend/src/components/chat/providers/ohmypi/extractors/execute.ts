import type { CommandResult } from '../../../model/commandResult'
import type { CommandLanguage, ExecuteRequest } from '../../../model/tools/execute'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { OH_MY_PI_EVAL_LANGUAGE } from '../protocol'

/**
 * What one finished `bash` call printed, and how it ended.
 *
 * omp appends its own notices to the command's output, each after a blank line
 * (`tools/bash.ts`, `#buildCompletedResult`):
 *
 *   <output>
 *
 *   Wall time: 0.05 seconds
 *
 *   Command exited with code 3
 *
 * A timeout adds `[Command timed out after N seconds]`, and a stop that the reader
 * asked for ends the text with `[Command aborted]` or opens it with
 * `[Command cancelled]`. The notices restate what `details` and the row's status
 * already say, so the body drops them and keeps the output alone.
 */
export interface OhMyPiCommandOutcome {
  result: CommandResult
  /** The reader stopped the command. */
  cancelled: boolean
  /** The command ran out of time. */
  timedOut: boolean
}

const WALL_TIME_NOTICE = /^Wall time: [\d.]+ seconds$/
const EXIT_CODE_NOTICE = /^Command exited with code (-?\d+)$/
const TIMEOUT_NOTICE = /^\[?Command timed out(?: after \d+ seconds)?\]?$/
const ABORT_NOTICE = /^\[?Command aborted\]?$/
const CANCEL_NOTICE = '[Command cancelled]'

/** Drop the blank lines at the end of a list of lines, in place. */
function trimTrailingBlank(lines: string[]): void {
  while (lines.length > 0 && lines.at(-1)?.trim() === '')
    lines.pop()
}

/**
 * Read one `bash` result into a command result.
 *
 * `details.exitCode` is present only for a code other than zero, so a call that ENDED
 * with no error and no code in its details exited with zero. `endedWithoutError` states
 * that: omp sent the call's end frame and flagged no error, and did not move the
 * command to the background. Every other result states only the code it reported --
 * a timeout, a stop, and the partial output of a turn that ended first state none.
 */
export function ohMyPiCommandOutcome(text: string, details: Record<string, unknown>, endedWithoutError: boolean): OhMyPiCommandOutcome {
  const lines = text.replace(/\r\n/g, '\n').split('\n')
  let exitCode: number | undefined = pickNumber(details, 'exitCode', undefined)
  let timedOut = details.timedOut === true
  let cancelled = false
  if (lines[0]?.startsWith(CANCEL_NOTICE)) {
    cancelled = true
    lines[0] = lines[0].slice(CANCEL_NOTICE.length).trimStart()
    if (lines[0] === '')
      lines.shift()
  }
  // The notices sit at the END, each after a blank line. Peel them off from the last
  // line upwards, and stop at the first line that is output.
  for (;;) {
    trimTrailingBlank(lines)
    const last = lines.at(-1)?.trim()
    if (last === undefined)
      break
    const code = EXIT_CODE_NOTICE.exec(last)
    if (code?.[1] !== undefined) {
      exitCode ??= Number(code[1])
      lines.pop()
      continue
    }
    if (WALL_TIME_NOTICE.test(last)) {
      lines.pop()
      continue
    }
    if (TIMEOUT_NOTICE.test(last)) {
      timedOut = true
      lines.pop()
      continue
    }
    if (ABORT_NOTICE.test(last)) {
      cancelled = true
      lines.pop()
      continue
    }
    break
  }
  if (exitCode === undefined && endedWithoutError && !timedOut && !cancelled)
    exitCode = 0
  const wallTimeMs = pickNumber(details, 'wallTimeMs', undefined)
  const truncated = isObject(pickObject(pickObject(details, 'meta'), 'truncation'))
  const result: CommandResult = {
    output: lines.join('\n'),
    ...(exitCode !== undefined ? { exitCode } : {}),
    ...(wallTimeMs !== undefined ? { durationMs: wallTimeMs } : {}),
    ...(truncated ? { truncated: true } : {}),
  }
  return { result, cancelled, timedOut }
}

/**
 * The cells one `eval` call ran, one command result each.
 *
 * omp states each cell in `details.cells` with its own output, status and exit code
 * (`EvalCellResult`). Its `index` counts from 0, and the label counts from 1. A call
 * that states no cells answers null, and the caller reads the result text instead.
 */
export function ohMyPiEvalResults(details: Record<string, unknown>): CommandResult[] | null {
  const cells = details.cells
  if (!Array.isArray(cells) || cells.length === 0)
    return null
  const results: CommandResult[] = []
  for (const [position, cell] of cells.entries()) {
    if (!isObject(cell))
      continue
    const exitCode = pickNumber(cell, 'exitCode', undefined)
    const durationMs = pickNumber(cell, 'durationMs', undefined)
    const title = pickString(cell, 'title')
    results.push({
      output: pickString(cell, 'output'),
      label: title || `Cell ${pickNumber(cell, 'index', position) + 1}`,
      ...(exitCode !== undefined ? { exitCode } : {}),
      ...(durationMs !== undefined ? { durationMs } : {}),
    })
  }
  return results.length > 0 ? results : null
}

/** The highlighter word for an `eval` cell, when the closed set holds one. */
function evalLanguage(language: string): CommandLanguage | undefined {
  return language === OH_MY_PI_EVAL_LANGUAGE.JavaScript ? 'javascript' : undefined
}

/**
 * The command one `eval` call runs: its code, the language that highlights it, and the
 * title the model gave the cell. Python has no highlighter in the closed set, so a
 * Python cell draws as plain text.
 */
export function ohMyPiEvalRequest(args: Record<string, unknown>): ExecuteRequest {
  const language = evalLanguage(pickString(args, 'language'))
  const title = pickString(args, 'title')
  return {
    command: pickString(args, 'code'),
    ...(language !== undefined ? { language } : {}),
    ...(title ? { description: title } : {}),
  }
}
