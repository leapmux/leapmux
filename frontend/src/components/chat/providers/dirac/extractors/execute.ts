import type { CommandExit } from '../../../model/commandResult'
import type { ToolCallSpec } from '../../../model/toolCall'
import type { ACPToolFacts } from '../../acp/extractors/toolCall'
import { pickObject } from '~/lib/jsonPick'
import { withCommandExit } from '../../../model/commandResult'

/** Read the exit fields beside Dirac's native command output. */
export function diracCommandExit(tool: Record<string, unknown>): CommandExit | undefined {
  const raw = pickObject(tool, 'rawOutput')
  if (!raw || typeof raw.output !== 'string')
    return undefined
  if (raw.userRejected === true)
    return {}
  const signal = raw.signal
  if (typeof signal === 'string' && signal.trim() !== '' && !signal.includes('\0'))
    return { signal }
  const code = raw.exitCode
  if (typeof code === 'number' && Number.isSafeInteger(code))
    return { exitCode: code }
  return code === null ? { exitCode: null } : {}
}

/** Keep the shared result and add Dirac's exit to its one inline command. */
export function diracExecuteSpec(facts: ACPToolFacts, base: () => ToolCallSpec): ToolCallSpec {
  const spec = base()
  if (spec.kind !== 'execute' || spec.result === undefined || !('commands' in spec.result)
    || spec.result.commands.length !== 1 || facts.terminals.entries.length > 0
    || facts.terminals.unresolved.length > 0 || spec.result.unresolvedTerminals.length > 0) {
    return spec
  }
  const exit = diracCommandExit(facts.tool)
  const command = spec.result.commands[0]
  if (exit === undefined || command === undefined)
    return spec
  const raw = pickObject(facts.tool, 'rawOutput')
  const text = typeof raw?.output === 'string' ? raw.output : undefined
  const prefix = exit.exitCode === 0
    ? 'Command executed successfully (exit code 0).\nOutput:\n'
    : typeof exit.exitCode === 'number' ? `Command failed with exit code ${exit.exitCode}.\nOutput:\n` : undefined
  const output = text !== undefined && prefix !== undefined && text.startsWith(prefix) ? text.slice(prefix.length) : command.output
  return { ...spec, result: { ...spec.result, commands: [withCommandExit({ ...command, output }, exit)] } }
}
