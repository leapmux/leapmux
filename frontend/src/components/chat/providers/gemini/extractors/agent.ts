import type { AgentRequest, AgentRun } from '../../../model/tools/agent'
import { clipFirstLine } from '~/lib/clipFirstLine'
import { pickObject, pickString } from '~/lib/jsonPick'
import { geminiResultText } from './results'

export function geminiAgentRequest(args: Record<string, unknown>): AgentRequest {
  const agentType = pickString(args, 'agent_name')
  const prompt = pickString(args, 'prompt')
  return { description: clipFirstLine(prompt, 80) || agentType || 'Subagent', prompt, ...(agentType ? { agentType } : {}) }
}

export function geminiAgentRun(record: Record<string, unknown>): AgentRun {
  const request = geminiAgentRequest(pickObject(record, 'args') ?? {})
  const display = pickObject(record, 'resultDisplay')
  const state = pickString(display, 'state')
  const reason = pickString(display, 'terminateReason')
  const outcome = state === 'cancelled' ? 'stopped' : state === 'error' || (reason !== '' && reason !== 'GOAL') ? 'failed' : state === 'completed' ? 'completed' : 'unknown'
  return {
    description: request.description,
    agentId: pickString(record, 'agentId'),
    outcome,
    metadata: reason ? [{ label: 'Termination reason', value: reason }] : [],
    body: pickString(display, 'result') || geminiResultText(record),
  }
}
