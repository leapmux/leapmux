import type { CommandExit } from '../../../model/commandResult'
import type { ToolCallSpec } from '../../../model/toolCall'
import type { ACPToolCallAdapter, ACPToolFacts } from '../../acp/extractors/toolCall'
import { withCommandExit } from '../../../model/commandResult'
import { declinedToolCallSpec } from '../../declinedToolCall'
import { isFastAgentRefusalSentence } from '../protocol'

/**
 * Read a Fast Agent call through the shared builder.
 * The adapter marks a refused call as declined and keeps its refusal text.
 * A command result reads the exit that Fast Agent writes after its output.
 */
export const fastAgentToolCallAdapter: ACPToolCallAdapter = (facts, base): ToolCallSpec => {
  const spec = base()
  return fastAgentRefused(facts) ? declinedToolCallSpec(spec, facts.text) : fastAgentCommandSpec(spec)
}

/** The final native exit block follows two separator line breaks. */
const FAST_AGENT_EXIT_BLOCK = /\n\n\[Exit code: (-?\d+)\]$/

/** Read the native exit block and omit it from the command output. */
function fastAgentCommandSpec(spec: ToolCallSpec): ToolCallSpec {
  if (spec.kind !== 'execute' || spec.result === undefined || !('commands' in spec.result) || spec.result.commands.length !== 1)
    return spec
  const command = spec.result.commands[0]
  const exit = command === undefined ? undefined : fastAgentCommandExit(command.output)
  if (command === undefined || exit === undefined)
    return spec
  const block = FAST_AGENT_EXIT_BLOCK.exec(command.output)
  const output = block ? command.output.slice(0, block.index) : command.output
  return { ...spec, result: { ...spec.result, commands: [withCommandExit({ ...command, output }, exit)] } }
}

/**
 * Read a safe integer exit code from the final native exit block.
 * Fast Agent states the code only in this block.
 * The shared Agent Client Protocol builder does not read that native text.
 */
export function fastAgentCommandExit(text: string): CommandExit | undefined {
  const block = FAST_AGENT_EXIT_BLOCK.exec(text)
  const exitCode = block ? Number(block[1]) : Number.NaN
  return Number.isSafeInteger(exitCode) ? { exitCode } : undefined
}

/**
 * Decide whether Fast Agent refused the call after a Deny answer.
 * The frame must state a failed update and the native refusal sentence.
 * A completed call can print the same sentence as its actual output.
 */
function fastAgentRefused(facts: ACPToolFacts): boolean {
  return facts.lifecycle.frameStatus === 'failed' && isFastAgentRefusalSentence(facts.text)
}
