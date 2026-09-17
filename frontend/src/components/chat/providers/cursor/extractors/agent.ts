import type { ToolCallPayload } from '../../../ir/toolCall'
import type { AgentRequest, AgentRun } from '../../../ir/tools/agent'
import type { ACPToolFacts } from '../../acp/extractors/toolCall'
import { ACP_SUPPLEMENT } from '~/generated/contracts/acp-protocol'
import { isObject, pickBoolean, pickObject, pickString } from '~/lib/jsonPick'
import { formatDuration } from '../../../rendererUtils'
import { collectAcpToolText } from '../../acp/content'

/**
 * The reader's name for each subagent type.
 *
 * Cursor spells one type three ways, and all three reach a row. Its stored record
 * writes `subagent_type`. Its protobuf oneof writes a camelCase KEY (`computerUse`).
 * Its `cursor/task` extension frame normalizes the same value to a snake_case WORD
 * (`computer_use`), which is why both spellings are listed.
 */
const AGENT_TYPES: Record<string, string> = {
  unspecified: 'General purpose',
  generalPurpose: 'General purpose',
  general_purpose: 'General purpose',
  explore: 'Explore',
  computerUse: 'Computer use',
  computer_use: 'Computer use',
  browserUse: 'Browser use',
  browser_use: 'Browser use',
  mediaReview: 'Media review',
  media_review: 'Media review',
  videoReview: 'Video review',
  video_review: 'Video review',
  watchVideo: 'Watch video',
  bash: 'Bash',
  shell: 'Shell',
  vmSetupHelper: 'VM setup helper',
  vm_setup_helper: 'VM setup helper',
  debug: 'Debug',
  cursorGuide: 'Cursor guide',
}

function agentType(input: Record<string, unknown>): string {
  const saved = pickString(input, 'subagent_type') || pickString(input, 'subagentType')
  if (saved)
    return pickString(AGENT_TYPES, saved) || saved
  const type = pickObject(input, 'subagentType')
  // Two custom shapes. The stored record writes the protobuf-JSON `{custom:{name}}`;
  // the extension frame writes the runtime's own `{custom:"<word>"}`.
  const custom = pickString(pickObject(type, 'custom'), 'name') || pickString(type, 'custom')
  if (custom)
    return pickString(AGENT_TYPES, custom) || custom
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

/**
 * Cursor's native result contains conversation steps that ACP omits.
 *
 * One whole agent payload: the request identifies the run the call asked for, and the
 * result reports the run the native record measured. `input` is the merged
 * arguments -- a caller that holds the `cursor/task` extension frame folds its
 * model, agent id and duration in first, so the row reports the run and not the
 * request.
 */
export function cursorAgentCall(
  facts: ACPToolFacts,
  input: Record<string, unknown>,
  native: Record<string, unknown> | null | undefined,
  savedOutput?: string,
): ToolCallPayload<'agent'> {
  const tool = facts.tool
  const raw = pickObject(tool, ACP_SUPPLEMENT.RawOutput)
  const success = pickObject(pickObject(native, 'output'), 'success')
  const nativeError = pickObject(pickObject(native, 'output'), 'error')
  const description = pickString(input, 'description') || pickString(tool, 'title').replace(/^Task: /, '')
  const metadata: AgentRun['metadata'] = []
  // The stored native record states these; a row whose record never arrived takes
  // them from the `cursor/task` extension frame, which the caller folds into input.
  const agentId = pickString(success, 'agentId') || pickString(input, 'agentId')
  const modelName = pickString(input, 'model')
  const transcript = pickString(success, 'transcriptPath')
  if (agentId)
    metadata.push({ label: 'Agent ID', value: agentId })
  if (modelName)
    metadata.push({ label: 'Model', value: modelName })
  if (transcript)
    metadata.push({ label: 'Transcript', value: transcript })
  const elapsed = duration(success?.durationMs ?? raw?.durationMs ?? input.durationMs)
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
  // `facts.finished`, never `acpToolFinished(tool)`: the frame alone cannot see the
  // turn's own outcome, so a retained subagent row read as still running and drew
  // the prompt with the agent's report dropped.
  const finished = facts.finished
  const report = [...reports, suffix].filter(Boolean).join('\n\n')
  const originalOutput = collectAcpToolText(tool, { rawObjects: false })
  const output = finished ? error || report || savedOutput || originalOutput : originalOutput
  const source: AgentRun = {
    description,
    agentId,
    statusLabel: status,
    outcome: status,
    metadata,
    body: output || (!background && !failed && !stopped ? 'The provider did not supply a report.' : ''),
  }
  const request: AgentRequest = {
    description,
    agentType: agentType(input),
    prompt: pickString(input, 'prompt'),
    metadata: modelName ? [{ label: 'Model', value: modelName }] : [],
  }
  return {
    kind: 'agent',
    label: 'Task',
    title: description || 'Task',
    request,
    ...(finished ? { result: { agents: [source] } } : {}),
  }
}
