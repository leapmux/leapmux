import type { AgentResultSource } from '../../results/agentResult'
import type { copilotNativeTool } from './nativeTool'
import { COPILOT_EVENT } from '~/generated/contracts/copilot-protocol'
import { pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { formatDuration, formatNumber } from '../../rendererUtils'

/** Keep launch instructions separate from the agent's report. */
export function copilotAgentResult(native: NonNullable<ReturnType<typeof copilotNativeTool>>, input: Record<string, unknown>, output: string, status: unknown): AgentResultSource {
  const finished = pickObject(native.finished, 'data')
  const started = pickObject(native.started, 'data')
  const agentId = pickString(native.finished, 'agentId') || pickString(native.started, 'agentId')
  const stopped = status === 'cancelled' || finished?.cancelled === true
  const failed = status === 'failed' || native.finished?.type === COPILOT_EVENT.SubagentFailed
  const launch = input.mode === 'background'
  const outcome = stopped ? 'stopped' : failed ? 'failed' : launch && !native.finished ? 'running' : 'completed'
  const metadata: AgentResultSource['metadata'] = []
  if (agentId)
    metadata.push({ label: 'Agent ID', value: agentId })
  const model = pickString(finished, 'model') || pickString(started, 'model') || pickString(input, 'model')
  if (model)
    metadata.push({ label: 'Model', value: model })
  for (const [label, field] of [['Tool uses', 'totalToolCalls'], ['Tokens', 'totalTokens'], ['Duration', 'durationMs']]) {
    const value = pickNumber(finished, field, undefined)
    if (value !== undefined && Number.isSafeInteger(value) && value >= 0)
      metadata.push({ label, value: label === 'Duration' ? formatDuration(value) : formatNumber(value) })
  }
  return {
    description: pickString(input, 'description') || pickString(input, 'name'),
    agentId,
    status: outcome === 'running' ? 'launched asynchronously' : outcome,
    outcome,
    metadata,
    body: launch && !failed ? pickString(input, 'prompt') : output,
    bodyLabel: launch && !failed ? 'Prompt' : undefined,
  }
}
