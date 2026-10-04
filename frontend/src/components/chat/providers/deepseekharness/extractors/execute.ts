import type { CommandResult } from '../../../model/commandResult'

/** Read the native Bash footer through the same rules as the native Web client. */
export function deepseekHarnessCommandResult(text: string): CommandResult {
  const signal = /\n\[killed by signal: ([^\]\n]+)\]$/.exec(text)
  if (signal?.[1] !== undefined)
    return { output: text.slice(0, signal.index), signal: signal[1] }
  const exit = /\n\[exit code: (\d+)\]$/.exec(text)
  if (exit?.[1] !== undefined) {
    const code = Number(exit[1])
    if (Number.isSafeInteger(code))
      return { output: text.slice(0, exit.index), exitCode: code }
  }
  return { output: text, exitCode: 0 }
}
