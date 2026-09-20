import type { AgentRequest, AgentRun, AgentRunStatus } from '../../../model/tools/agent'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { CODEX_COLLAB_ITEM, CODEX_ITEM } from '~/generated/contracts/codex-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { pickObject, pickString, stringArray } from '~/lib/jsonPick'
import { getCachedSettingsLabel } from '~/lib/settingsLabelCache'
import { messageCompletionFromProto } from '../../../assembledMessage'
import { CODEX_STATUS } from '../itemVocabulary'
import { extractItem } from './item'

/** Only a matching call with the expected role can supply missing fields. */
export function codexAgentCounterpart(item: Record<string, unknown>, parsed: ParsedMessageContent | undefined, role: 'request' | 'result'): Record<string, unknown> | null {
  const candidate = extractItem(parsed?.parentObject)
  const id = pickString(item, 'id')
  if (!id || candidate?.id !== id || candidate.type !== CODEX_ITEM.CollabAgentToolCall)
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
  // An EMPTY string counts as absent, exactly as a missing key does. Codex serializes an
  // unset String as `""`, so a nullish test alone kept the empty field and the row lost
  // the prompt, the model or the tool name that its counterpart carried. The
  // `receiverThreadIds` test below already reads emptiness this way.
  for (const field of ['tool', 'prompt', 'model', 'reasoningEffort']) {
    if (resolved[field] == null || resolved[field] === '')
      resolved[field] = counterpart[field]
  }
  if (!stringArray(resolved[CODEX_COLLAB_ITEM.ReceiverThreadIDs]).some(id => id.trim() !== ''))
    resolved[CODEX_COLLAB_ITEM.ReceiverThreadIDs] = counterpart[CODEX_COLLAB_ITEM.ReceiverThreadIDs]
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

function agentMetadata(item: Record<string, unknown>): AgentRun['metadata'] {
  const model = pickString(item, 'model')
  const effort = pickString(item, 'reasoningEffort')
  return [
    ...(model ? [{ label: 'Model', value: getCachedSettingsLabel(AgentProvider.CODEX, 'model', model) || model }] : []),
    ...(effort ? [{ label: 'Reasoning effort', value: getCachedSettingsLabel(AgentProvider.CODEX, 'effort', effort) || effort }] : []),
  ]
}

export function codexAgentRequest(item: Record<string, unknown>): AgentRequest {
  const tool = pickString(item, 'tool')
  const targets = stringArray(item[CODEX_COLLAB_ITEM.ReceiverThreadIDs]).filter(id => id.trim() !== '')
  // A SPAWN with one target created one background task, and that task's title says
  // what the subagent does. Without it every spawn row reads "Subagent", because the
  // description above is the tool's own label. The other tools act ON agents that
  // already exist, so none of them created the row a title could come from.
  const registryKey = tool === 'spawnAgent' && targets.length === 1 ? targets[0] : undefined
  return {
    description: pickString(TOOL_LABELS, tool) || tool || 'Agent tool',
    prompt: pickString(item, 'prompt'),
    metadata: [...targets.map(value => ({ label: 'Agent ID', value })), ...agentMetadata(item)],
    ...(registryKey !== undefined ? { registryKey } : {}),
  }
}

/** Each native child state as the two words a run's card states: its label and its outcome. */
const AGENT_STATES: Record<string, { statusLabel: string, outcome: AgentRunStatus }> = {
  pendingInit: { statusLabel: 'starting', outcome: 'running' },
  running: { statusLabel: 'running', outcome: 'running' },
  completed: { statusLabel: 'completed', outcome: 'completed' },
  errored: { statusLabel: 'failed', outcome: 'failed' },
  interrupted: { statusLabel: 'interrupted', outcome: 'stopped' },
  shutdown: { statusLabel: 'stopped', outcome: 'stopped' },
  notFound: { statusLabel: 'not found', outcome: 'failed' },
}

/** The call's completion does not establish that any child agent completed. */
export function codexAgentResults(item: Record<string, unknown>): AgentRun[] {
  const states = pickObject(item, CODEX_COLLAB_ITEM.AgentsStates) ?? {}
  const ids = [...new Set([...stringArray(item[CODEX_COLLAB_ITEM.ReceiverThreadIDs]), ...Object.keys(states)])].filter(id => id.trim() !== '')
  return ids.map((id) => {
    const state = pickObject(states, id)
    const nativeStatus = pickString(state, 'status')
    const launch = item.tool === 'spawnAgent' && item.status === 'completed'
    const knownState = Object.hasOwn(AGENT_STATES, nativeStatus) ? AGENT_STATES[nativeStatus] : undefined
    const resolved = knownState ?? (launch && !nativeStatus
      ? { statusLabel: 'launched asynchronously', outcome: 'running' as const }
      : { statusLabel: nativeStatus || 'status unavailable', outcome: 'unknown' as const })
    const report = pickString(state, 'message')
    const showPrompt = launch && resolved.outcome === 'running' && !report
    return {
      ...resolved,
      description: '',
      agentId: id,
      registryKey: id,
      metadata: [{ label: 'Agent ID', value: id }, ...agentMetadata(item)],
      body: report || (showPrompt ? pickString(item, 'prompt') : ''),
      ...(showPrompt ? { bodyLabel: 'Prompt' } : {}),
    }
  })
}
