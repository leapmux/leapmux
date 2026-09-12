import type { AgentRequestSource } from '../../../results/AgentRequestMessage'
import type { AgentResultSource } from '../../../results/agentResult'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { pickObject, pickString, stringArray } from '~/lib/jsonPick'
import { getCachedSettingsLabel } from '~/lib/settingsLabelCache'
import { CODEX_ITEM, CODEX_STATUS } from '~/types/toolMessages'
import { messageCompletionFromProto } from '../../../assembledMessage'
import { extractItem } from '../renderHelpers'

/** Only a matching call with the expected role can supply missing fields. */
export function codexAgentCounterpart(item: Record<string, unknown>, parsed: ParsedMessageContent | undefined, role: 'request' | 'result'): Record<string, unknown> | null {
  const candidate = extractItem(parsed?.parentObject)
  const id = pickString(item, 'id')
  if (!id || candidate?.id !== id || candidate.type !== CODEX_ITEM.COLLAB_AGENT_TOOL_CALL)
    return null
  const tool = pickString(item, 'tool')
  const otherTool = pickString(candidate, 'tool')
  if (tool && otherTool && tool !== otherTool)
    return null
  const finished = messageCompletionFromProto(parsed?.completion) !== null || Number.isFinite(parsed?.parentObject?.completedAtMs)
    || ['completed', 'failed', 'interrupted'].includes(pickString(candidate, 'status'))
  return (role === 'result' ? finished : !finished && candidate.status === CODEX_STATUS.IN_PROGRESS) ? candidate : null
}

/** The current item owns status and reports. Its counterpart can supply omitted request fields. */
export function resolveCodexAgentItem(item: Record<string, unknown>, counterpart: Record<string, unknown> | null): Record<string, unknown> {
  if (!counterpart)
    return item
  const resolved = { ...item }
  for (const field of ['tool', 'prompt', 'model', 'reasoningEffort']) {
    resolved[field] ??= counterpart[field]
  }
  if (!stringArray(resolved.receiverThreadIds).some(id => id.trim() !== ''))
    resolved.receiverThreadIds = counterpart.receiverThreadIds
  return resolved
}

const TOOL_LABELS: Record<string, string> = {
  spawnAgent: 'Subagent',
  sendInput: 'Send input to agent',
  resumeAgent: 'Resume agent',
  wait: 'Wait for agents',
  closeAgent: 'Close agent',
  sendMessage: 'Send message to agent',
  followupTask: 'Send task to agent',
  interruptAgent: 'Interrupt agent',
  listAgents: 'List agents',
}

function agentMetadata(item: Record<string, unknown>): AgentResultSource['metadata'] {
  const model = pickString(item, 'model')
  const effort = pickString(item, 'reasoningEffort')
  return [
    ...(model ? [{ label: 'Model', value: getCachedSettingsLabel(AgentProvider.CODEX, 'model', model) || model }] : []),
    ...(effort ? [{ label: 'Reasoning effort', value: getCachedSettingsLabel(AgentProvider.CODEX, 'effort', effort) || effort }] : []),
  ]
}

export function codexAgentRequest(item: Record<string, unknown>): AgentRequestSource {
  const tool = pickString(item, 'tool')
  const targets = stringArray(item.receiverThreadIds).filter(id => id.trim() !== '')
  return {
    toolName: tool || 'Agent tool',
    description: pickString(TOOL_LABELS, tool) || tool || 'Agent tool',
    prompt: pickString(item, 'prompt'),
    metadata: [...targets.map(value => ({ label: 'Agent ID', value })), ...agentMetadata(item)],
  }
}

const AGENT_STATES: Record<string, { status: string, outcome: AgentResultSource['outcome'] }> = {
  pendingInit: { status: 'starting', outcome: 'running' },
  running: { status: 'running', outcome: 'running' },
  completed: { status: 'completed', outcome: 'completed' },
  errored: { status: 'failed', outcome: 'failed' },
  interrupted: { status: 'interrupted', outcome: 'stopped' },
  shutdown: { status: 'stopped', outcome: 'stopped' },
  notFound: { status: 'not found', outcome: 'failed' },
}

/** The call's completion does not establish that any child agent completed. */
export function codexAgentResults(item: Record<string, unknown>): AgentResultSource[] {
  const states = pickObject(item, 'agentsStates') ?? {}
  const ids = [...new Set([...stringArray(item.receiverThreadIds), ...Object.keys(states)])].filter(id => id.trim() !== '')
  return ids.map((id) => {
    const state = pickObject(states, id)
    const nativeStatus = pickString(state, 'status')
    const launch = item.tool === 'spawnAgent' && item.status === 'completed'
    const knownState = Object.hasOwn(AGENT_STATES, nativeStatus) ? AGENT_STATES[nativeStatus] : undefined
    const resolved = knownState ?? (launch && !nativeStatus
      ? { status: 'launched asynchronously', outcome: 'running' as const }
      : { status: nativeStatus || 'status unavailable', outcome: 'unknown' as const })
    const report = pickString(state, 'message')
    const showPrompt = launch && resolved.outcome === 'running' && !report
    return {
      ...resolved,
      description: '',
      agentId: id,
      registryKey: id,
      metadata: [{ label: 'Agent ID', value: id }, ...agentMetadata(item)],
      body: report || (showPrompt ? pickString(item, 'prompt') : ''),
      bodyLabel: showPrompt ? 'Prompt' : undefined,
    }
  })
}
