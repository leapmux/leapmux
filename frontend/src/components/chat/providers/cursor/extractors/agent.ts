import type { ToolCallSpecVariant } from '../../../model/toolCall'
import type { AgentRequest, AgentRun } from '../../../model/tools/agent'
import type { ACPToolFacts } from '../../acp/extractors/toolCall'
import { ACP_SUPPLEMENT } from '~/generated/contracts/acp-protocol'
import { isObject, pickBoolean, pickObject, pickString } from '~/lib/jsonPick'
import { formatDuration } from '../../../rendererUtils'
import { collectAcpToolText } from '../../acp/content'

/**
 * The reader's name for each subagent type.
 *
 * Cursor supplies three representations of the same type:
 * - The stored record uses subagent_type.
 * - The protobuf oneof uses a camelCase key, such as computerUse.
 * - The cursor/task extension uses a snake_case value, such as computer_use.
 * Keep both spellings because all three representations reach a row.
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
  // The stored record uses the protobuf JSON shape {custom:{name}}.
  // The extension frame uses the native shape {custom:"<word>"}.
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
 * The request identifies the requested run. The result describes the run that the native record measured.
 * input contains merged arguments. A cursor/task caller first merges these fields from its extension frame:
 * - Model.
 * - Agent ID.
 * - Duration.
 * The result row therefore describes the measured run. nativeReport supplies a fallback from the stored native tool record.
 */
export function cursorAgentCall(
  facts: ACPToolFacts,
  input: Record<string, unknown>,
  native: Record<string, unknown> | null | undefined,
  nativeReport?: string,
): ToolCallSpecVariant<'agent'> {
  const tool = facts.tool
  const raw = pickObject(tool, ACP_SUPPLEMENT.RawOutput)
  const success = pickObject(pickObject(native, 'output'), 'success')
  const nativeError = pickObject(pickObject(native, 'output'), 'error')
  const description = pickString(input, 'description') || pickString(tool, 'title').replace(/^Task: /, '')
  const metadata: AgentRun['metadata'] = []
  // Use the stored native record first. Without that record, use the cursor/task fields that the caller merged into input.
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
  // Use facts.finished because the frame alone cannot determine the turn's outcome.
  // A frame-only check can retain a running prompt after completion and omit the report.
  const finished = facts.finished
  const report = [...reports, suffix].filter(Boolean).join('\n\n')
  const originalOutput = collectAcpToolText(tool, { rawObjects: false })
  const output = finished ? error || report || nativeReport || originalOutput : originalOutput
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
