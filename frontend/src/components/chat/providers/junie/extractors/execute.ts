import type { CommandExit } from '../../../model/commandResult'
import type { ToolCallSpec } from '../../../model/toolCall'
import type { ACPToolCallAdapter, ACPToolFacts } from '../../acp/extractors/toolCall'
import { JUNIE_TERMINAL_META } from '~/generated/contracts/junie-protocol'
import { pickObject } from '~/lib/jsonPick'
import { withCommandExit } from '../../../model/commandResult'

/**
 * Read the native exit of one Junie command.
 * Junie states the code or signal in `_meta.terminal_exit`.
 * It repeats the code beside the output as `rawOutput.exitCode`.
 * Read the terminal exit first because it alone states a signal.
 * The shared Agent Client Protocol builder does not read these native fields.
 */
export function junieCommandExit(tool: Record<string, unknown>): CommandExit | undefined {
  const terminal = pickObject(pickObject(tool, '_meta'), JUNIE_TERMINAL_META.Exit)
  const terminalCode = terminal?.[JUNIE_TERMINAL_META.ExitCode]
  if (typeof terminalCode === 'number' && Number.isSafeInteger(terminalCode))
    return { exitCode: terminalCode }
  const signal = terminal?.[JUNIE_TERMINAL_META.Signal]
  if (typeof signal === 'string' && signal.trim() !== '')
    return { signal }
  const outputCode = pickObject(tool, 'rawOutput')?.exitCode
  return typeof outputCode === 'number' && Number.isSafeInteger(outputCode) ? { exitCode: outputCode } : undefined
}

/** Keep the shared result and add Junie's exit to its one inline command. */
export function junieExecuteSpec(facts: ACPToolFacts, base: () => ToolCallSpec): ToolCallSpec {
  const spec = base()
  if (spec.kind !== 'execute' || spec.result === undefined || !('commands' in spec.result)
    || spec.result.commands.length !== 1 || facts.terminals.entries.length > 0
    || facts.terminals.unresolved.length > 0 || spec.result.unresolvedTerminals.length > 0) {
    return spec
  }
  const exit = junieCommandExit(facts.tool)
  const command = spec.result.commands[0]
  if (exit === undefined || command === undefined)
    return spec
  return { ...spec, result: { ...spec.result, commands: [withCommandExit(command, exit)] } }
}

/** Junie's own reading of one call: the shared build, with the exit of a command. */
export const junieToolCallAdapter: ACPToolCallAdapter = (facts, base) => facts.wireKind === 'execute' ? junieExecuteSpec(facts, base) : base()
