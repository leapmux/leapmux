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
  return kind
    ? pickString(AGENT_TYPES, kind) || kind
    : ''
}

/** Protobuf JSON stores duration_ms as a decimal string. Keep large values exact. */
function duration(value: unknown): string | undefined {
  if (typeof value !== 'number' && (typeof value !== 'string' || !/^\d+$/.test(value)))
    return undefined
  const number = Number(value)
  if (!Number.isFinite(number) || number < 0 || !Number.isInteger(number))
    return undefined
  return Number.isSafeInteger(number)
    ? formatDuration(number)
    : `${value}ms`
}

/** What one finished launch states about its run, beside the arguments of the call. */
export interface CursorAgentRunSources {
  /** Cursor's own stored record of the call, which measured the run. */
  native?: Record<string, unknown> | null
  /** The output of the stored record, which reports the run when its steps state none. */
  nativeReport?: string
  /**
   * The fields that the `cursor/task` extension frame reports about the run: the model
   * that answered, the agent id and the measured duration. Only the result states them.
   */
  reported?: Record<string, unknown>
}

/**
 * Cursor's native result contains conversation steps that ACP omits.
 *
 * The request identifies the requested run, from the arguments of the call alone. The
 * result describes the run that the native record or the extension frame measured, so
 * a model that answered never reads as the model that the call asked for.
 */
export function cursorAgentCall(
  facts: ACPToolFacts,
  input: Record<string, unknown>,
  run: CursorAgentRunSources = {},
): ToolCallSpecVariant<'agent'> {
  const tool = facts.tool
  const raw = pickObject(tool, ACP_SUPPLEMENT.RawOutput)
  const reported = run.reported ?? {}
  const success = pickObject(pickObject(run.native, 'output'), 'success')
  const nativeError = pickObject(pickObject(run.native, 'output'), 'error')
  const description = pickString(input, 'description') || pickString(tool, 'title').replace(/^Task: /, '')
  const metadata: AgentRun['metadata'] = []
  // The stored native record first, then the extension frame, then the arguments.
  const agentId = pickString(success, 'agentId') || pickString(reported, 'agentId') || pickString(input, 'agentId')
  const requestedModel = pickString(input, 'model')
  const modelName = pickString(reported, 'model') || requestedModel
  const transcript = pickString(success, 'transcriptPath')
  if (agentId)
    metadata.push({ label: 'Agent ID', value: agentId })
  if (modelName)
    metadata.push({ label: 'Model', value: modelName })
  if (transcript)
    metadata.push({ label: 'Transcript', value: transcript })
  const elapsed = duration(success?.durationMs ?? raw?.durationMs ?? reported.durationMs ?? input.durationMs)
  if (elapsed !== undefined)
    metadata.push({ label: 'Duration', value: elapsed })
  const background = pickBoolean(success, 'isBackground') ?? pickBoolean(raw, 'isBackground') ?? false
  const failed = tool.status === 'failed' || run.native?.isError === true || nativeError !== null
  const stopped = tool.status === 'cancelled'
  const status = stopped
    ? 'stopped'
    : failed
      ? 'failed'
      : background ? 'running' : 'completed'
  const reports = Array.isArray(success?.conversationSteps)
    ? success.conversationSteps.map(step => pickString(isObject(step)
        ? pickObject(step, 'assistantMessage')
        : undefined, 'text')).filter(Boolean)
    : []
  const suffix = pickString(success, 'resultSuffix')
  const error = pickString(nativeError, 'error') || pickString(raw, 'error')
  // Use facts.finished because the frame alone cannot determine the turn's outcome.
  // A frame-only check can retain a running prompt after completion and omit the report.
  const finished = facts.finished
  const report = [...reports, suffix].filter(Boolean).join('\n\n')
  const originalOutput = collectAcpToolText(tool, { rawObjects: false })
  const output = finished
    ? error || report || run.nativeReport || originalOutput
    : originalOutput
  const source: AgentRun = {
    description,
    agentId,
    statusLabel: status,
    outcome: status,
    metadata,
    body: output || (!background && !failed && !stopped
      ? 'The provider did not supply a report.'
      : ''),
  }
  const request: AgentRequest = {
    description,
    agentType: agentType(input),
    prompt: pickString(input, 'prompt'),
    metadata: requestedModel
      ? [
          { label: 'Model', value: requestedModel },
        ]
      : [],
  }
  return {
    kind: 'agent',
    label: 'Task',
    title: description || 'Task',
    request,
    ...(finished
      ? {
          result: {
            agents: [source],
          },
        }
      : {}),
  }
}
