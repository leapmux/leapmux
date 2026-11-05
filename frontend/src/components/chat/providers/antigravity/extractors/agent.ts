import type { AgentRequest, AgentResult } from '../../../model/tools/agent'
import type { AntigravityStep } from './toolCommon'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

export function antigravityAgentRequest(step: AntigravityStep): AgentRequest {
  return {
    description: pickString(step.args, 'Role') || 'Subagent',
    agentType: pickString(step.args, 'TypeName'),
    prompt: pickString(step.args, 'Prompt'),
    promptFormat: 'markdown',
    ...(pickString(step.args, 'conversationId')
      ? {
          registryKey: pickString(step.args, 'conversationId'),
        }
      : {}),
  }
}

export function antigravityAgentResult(step: AntigravityStep, request: AgentRequest): AgentResult {
  const invoke = pickObject(step.raw, 'invokeSubagent')
  const results = Array.isArray(invoke?.results)
    ? invoke.results
    : []
  const agents = results.filter(isObject).map(result => ({
    description: request.description,
    ...(pickString(result, 'conversationId')
      ? {
          registryKey: pickString(result, 'conversationId'),
        }
      : {}),
    agentId: pickString(result, 'conversationId'),
    outcome: 'completed' as const,
    metadata: [],
    body: '',
  }))
  return { agents }
}
