import type { AgentRequestSource } from '../../results/AgentRequestMessage'
import type { AgentResultSource } from '../../results/agentResult'
import { clipFirstLine } from '~/lib/clipFirstLine'
import { prettifyJson } from '~/lib/jsonFormat'
import { pickNumber, pickObject, pickString, stringArray } from '~/lib/jsonPick'

export function gooseAgentRequest(input: Record<string, unknown>): AgentRequestSource {
  const instructions = pickString(input, 'instructions')
  const reference = pickString(input, 'context')
  const model = pickString(input, 'model')
  const provider = pickString(input, 'provider')
  const directory = pickString(input, 'working_dir')
  const turns = pickNumber(input, 'max_turns', undefined)
  const parameters = pickObject(input, 'parameters')
  const extensions = stringArray(input.extensions).filter(Boolean)
  return {
    toolName: 'Delegate',
    description: clipFirstLine(instructions, 80) || pickString(input, 'source') || 'Delegate task',
    agentType: instructions ? pickString(input, 'source') : undefined,
    prompt: reference ? `# Reference Context\n\n${reference}${instructions ? `\n\n# Task Instructions\n\n${instructions}` : ''}` : instructions,
    metadata: [
      ...(model ? [{ label: 'Model', value: model }] : []),
      ...(provider ? [{ label: 'Provider', value: provider }] : []),
      ...(directory ? [{ label: 'Working directory', value: directory }] : []),
      ...(turns !== undefined ? [{ label: 'Maximum turns', value: String(turns) }] : []),
      ...(parameters ? [{ label: 'Parameters', value: prettifyJson(parameters) }] : []),
      ...(extensions.length ? [{ label: 'Extensions', value: extensions.join(', ') }] : []),
    ],
  }
}

/** Goose's asynchronous acknowledgement repeats its session ID in the load command. */
const BACKGROUND_ACK = /^Task ([^\s"]+) started in background: "[\s\S]*"\nContinue with other work\. When you need the result, use load\(source: "\1"\)\.$/

export function gooseAgentResult(input: Record<string, unknown>, output: string, status: unknown): AgentResultSource {
  const request = gooseAgentRequest(input)
  const stopped = status === 'cancelled'
  const failed = status === 'failed'
  const acknowledgement = input.async === true && !failed && !stopped ? BACKGROUND_ACK.exec(output) : null
  const outcome = stopped ? 'stopped' : failed ? 'failed' : acknowledgement ? 'running' : input.async === true ? 'unknown' : 'completed'
  const agentId = acknowledgement?.[1] ?? ''
  return {
    description: request.description,
    agentId,
    status: acknowledgement ? 'launched asynchronously' : outcome === 'unknown' ? 'returned a result' : outcome,
    outcome,
    metadata: [...(agentId ? [{ label: 'Agent ID', value: agentId }] : []), ...(request.metadata ?? [])],
    body: acknowledgement ? request.prompt : output,
    bodyLabel: acknowledgement ? 'Prompt' : undefined,
  }
}
