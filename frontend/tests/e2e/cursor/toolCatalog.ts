import { readFileSync, realpathSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { requireBinary } from '../helpers/binaryOnPath'
import { hubSpawnEnv } from '../helpers/server'

/** Read the complete generated native tool union without executing the installed CLI module. */
export function cursorToolCasesFromBundle(source: string): string[] {
  const descriptors = [...source.matchAll(/static \$\(\)\{return\[("(?:[^"\\]|\\.)*")/g)]
    .map(match => JSON.parse(match[1]!) as string)
    .filter(value => value.startsWith('ToolCall|') && value.includes('19 task_tool_call '))
  if (descriptors.length !== 1)
    throw new Error('The installed Cursor bundle has no unique native ToolCall schema.')
  const cases = descriptors[0]!.split('|').slice(1).flatMap((field) => {
    const match = /^\d+ ([a-z0-9_]+) .+ tool$/.exec(field)
    return match ? [match[1]!] : []
  })
  if (cases.length === 0 || new Set(cases).size !== cases.length)
    throw new Error('The installed Cursor tool schema is empty or repeats a case.')
  return cases
}

/** Read the tool cases of the Cursor CLI that a Worker started with the agent environment finds. */
export function readInstalledCursorToolCases(environment: Record<string, string | undefined>): string[] {
  const binary = requireBinary('cursor-agent', 'The native Cursor catalog requires its installed CLI', hubSpawnEnv(environment))
  return cursorToolCasesFromBundle(readFileSync(join(dirname(realpathSync(binary)), 'index.js'), 'utf8'))
}
