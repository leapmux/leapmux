import type { AgentRequest, AgentResult, AgentRun } from '../../../model/tools/agent'
import type { AntigravityStep } from './toolCommon'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

export function antigravityAgentRequest(step: AntigravityStep): AgentRequest {
  const conversationId = pickString(step.args, 'conversationId')
  return {
    description: pickString(step.args, 'Role').trim() || 'Subagent',
    agentType: pickString(step.args, 'TypeName'),
    prompt: pickString(step.args, 'Prompt'),
    promptFormat: 'markdown',
    ...(conversationId
      ? {
          registryKey: conversationId,
        }
      : {}),
  }
}

/**
 * Build the launched `AgentRun` entries for a finished `invoke_subagent` step.
 *
 * Antigravity finishes this step as soon as the child cascade starts, so the step
 * reports a launch rather than a finished child run. The child's own transcript and
 * background-task row track its progress and final outcome.
 */
export function antigravityAgentResult(step: AntigravityStep, request: AgentRequest): AgentResult {
  const invoke = pickObject(step.raw, 'invokeSubagent')
  const subagents = Array.isArray(invoke?.subagents)
    ? invoke.subagents
    : []
  const results = Array.isArray(invoke?.results)
    ? invoke.results
    : []
  const fallbackDescription = request.description.trim()
  const agents: AgentRun[] = []
  for (const [position, result] of results.entries()) {
    if (!isObject(result))
      continue
    const subagent = subagents[position]
    const role = isObject(subagent)
      ? pickString(subagent, 'role').trim()
      : ''
    const conversationId = pickString(result, 'conversationId')
    agents.push({
      description: role || fallbackDescription,
      ...(conversationId
        ? {
            registryKey: conversationId,
          }
        : {}),
      agentId: conversationId,
      statusLabel: 'launched asynchronously',
      outcome: 'running',
      metadata: [],
      body: '',
    })
  }
  return { agents }
}
