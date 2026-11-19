import { readFileSync, realpathSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { requireBinary } from '../helpers/binaryOnPath'
import { hubSpawnEnv } from '../helpers/server'

/** Read every native client execution request without executing the installed CLI module. */
export function cursorExecutionCasesFromBundle(source: string): string[] {
  const descriptors = [
    ...source.matchAll(/static \$\(\)\{return\[("(?:[^"\\]|\\.)*")/g),
  ]
    .map(match => JSON.parse(match[1]!) as string)
    .filter(value => value.startsWith('ExecServerMessage|'))
  if (descriptors.length !== 1)
    throw new Error('The installed Cursor bundle has no unique native ExecServerMessage schema.')
  const cases = descriptors[0]!.split('|').slice(1).flatMap((field) => {
    const match = /^\d+ ([a-z0-9_]+) .+ message$/.exec(field)
    return match
      ? [match[1]!]
      : []
  })
  if (cases.length === 0 || new Set(cases).size !== cases.length)
    throw new Error('The installed Cursor execution schema is empty or repeats a case.')
  return cases
}

/** Read execution requests from the Cursor CLI that the Worker's agent environment selects. */
export function readInstalledCursorExecutionCases(environment: Record<string, string | undefined>): string[] {
  const binary = requireBinary('cursor-agent', 'The native Cursor catalog requires its installed CLI', hubSpawnEnv(environment))
  return cursorExecutionCasesFromBundle(readFileSync(join(dirname(realpathSync(binary)), 'index.js'), 'utf8'))
}
