import type { AgentResultSource } from '../../results/agentResult'
import type { ToolPresentation } from '~/components/chat/results/toolPresentation'
import { isObject, pickBoolean, pickObject, pickString } from '~/lib/jsonPick'
import { formatDuration } from '../../rendererUtils'
import { collectAcpToolText } from '../acp/content'
import { acpToolFinished } from '../acp/toolPresentation'

const AGENT_TYPES: Record<string, string> = {
  unspecified: 'General purpose',
  generalPurpose: 'General purpose',
  explore: 'Explore',
  computerUse: 'Computer use',
  browserUse: 'Browser use',
  mediaReview: 'Media review',
  watchVideo: 'Watch video',
  bash: 'Bash',
  shell: 'Shell',
  vmSetupHelper: 'VM setup helper',
  debug: 'Debug',
  cursorGuide: 'Cursor guide',
}

function agentType(input: Record<string, unknown>): string {
  const saved = pickString(input, 'subagent_type')
  if (saved)
    return pickString(AGENT_TYPES, saved) || saved
  const type = pickObject(input, 'subagentType')
  const custom = pickString(pickObject(type, 'custom'), 'name')
  if (custom)
    return custom
  const kind = type && Object.keys(type).find(key => isObject(type[key]))
  return kind ? pickString(AGENT_TYPES, kind) || kind : ''
}

/** Protobuf JSON stores duration_ms as a decimal string. Keep large values exact. */
function duration(value: unknown): string | undefined {
  if (typeof value !== 'number' && (typeof value !== 'string' || !/^\d+$/.test(value)))
    return undefined
  const number = Number(value)
  if (!Number.isFinite(number) || number < 0 || !Number.isInteger(number))
    return undefined
  return Number.isSafeInteger(number) ? formatDuration(number) : `${value}ms`
}

/** Cursor's native result contains conversation steps that ACP omits. */
export function cursorAgentPresentation(
  tool: Record<string, unknown>,
  model: ToolPresentation,
  native?: Record<string, unknown> | null,
  savedOutput?: string,
): ToolPresentation {
  const raw = pickObject(tool, 'rawOutput')
  const success = pickObject(pickObject(native, 'output'), 'success')
  const nativeError = pickObject(pickObject(native, 'output'), 'error')
  const input = model.input
  const description = pickString(input, 'description') || pickString(tool, 'title').replace(/^Task: /, '')
  const metadata: AgentResultSource['metadata'] = []
  const agentId = pickString(success, 'agentId')
  const modelName = pickString(input, 'model')
  const transcript = pickString(success, 'transcriptPath')
  if (agentId)
    metadata.push({ label: 'Agent ID', value: agentId })
  if (modelName)
    metadata.push({ label: 'Model', value: modelName })
  if (transcript)
    metadata.push({ label: 'Transcript', value: transcript })
  const elapsed = duration(success?.durationMs ?? raw?.durationMs)
  if (elapsed !== undefined)
    metadata.push({ label: 'Duration', value: elapsed })
  const background = pickBoolean(success, 'isBackground') ?? pickBoolean(raw, 'isBackground') ?? false
  const failed = tool.status === 'failed' || native?.isError === true || nativeError !== null
  const stopped = tool.status === 'cancelled'
  const status = stopped ? 'stopped' : failed ? 'failed' : background ? 'running' : 'completed'
  const reports = Array.isArray(success?.conversationSteps)
    ? success.conversationSteps.map(step => pickString(isObject(step) ? pickObject(step, 'assistantMessage') : undefined, 'text')).filter(Boolean)
    : []
  const suffix = pickString(success, 'resultSuffix')
  const error = pickString(nativeError, 'error') || pickString(raw, 'error')
  const finished = acpToolFinished(tool)
  const report = [...reports, suffix].filter(Boolean).join('\n\n')
  const originalOutput = collectAcpToolText(tool, { rawObjects: false })
  const output = finished ? error || report || savedOutput || originalOutput : originalOutput
  const source: AgentResultSource = {
    description,
    agentId,
    status,
    outcome: status,
    metadata,
    body: output || (!background && !failed && !stopped ? 'The provider did not supply a report.' : ''),
  }
  return {
    ...model,
    kind: 'agent',
    title: description || 'Task',
    agentRequest: { toolName: 'Task', description, agentType: agentType(input), prompt: pickString(input, 'prompt'), metadata: modelName ? [{ label: 'Model', value: modelName }] : [] },
    output,
    body: finished ? { type: 'agent', source } : { type: 'text' },
  }
}
