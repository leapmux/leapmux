import type { CommandResult } from '../../../model/commandResult'
import type { ExecuteRequest } from '../../../model/tools/execute'
import type { AntigravityStep } from './toolCommon'
import { pickNumber, pickObject, pickString } from '~/lib/jsonPick'

export function antigravityExecuteRequest(step: AntigravityStep): ExecuteRequest {
  const run = pickObject(step.raw, 'runCommand')
  return {
    command: pickString(step.args, 'CommandLine') || pickString(run, 'commandLine'),
    ...(pickString(run, 'cwd')
      ? {
          cwd: pickString(run, 'cwd'),
        }
      : {}),
  }
}

export function antigravityCommandOutput(output: string): string {
  const marker = /^Command output:\r?\n/m.exec(output)
  if (!marker || marker.index === undefined)
    return output
  const prefix = output.slice(0, marker.index)
    .replace(/^Created At:[^\n]*\n/m, '')
    .replace(/^Completed At:[^\n]*\n/m, '')
    .replace(/^Command exited with code[^\n]*\n/m, '')
    .replace(/^Working directory:[^\n]*\n/m, '')
  return prefix + output.slice(marker.index + marker[0].length)
}

export function antigravityCommandResult(step: AntigravityStep): CommandResult {
  const run = pickObject(step.raw, 'runCommand')
  const output = antigravityCommandOutput(pickString(pickObject(run, 'combinedOutput'), 'full'))
  const exitCode = pickNumber(run, 'exitCode', undefined)
  return {
    output,
    ...(exitCode !== undefined
      ? { exitCode }
      : {}),
  }
}
