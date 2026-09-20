import type { AgentRequest, AgentRun } from '../../../model/tools/agent'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { prettifyArgsJson } from '~/lib/jsonFormat'
import { pickString } from '~/lib/jsonPick'
import { piExtractTool, piPairedRequest, piPairedResult } from './toolCommon'

/** The launch acknowledgement supplies the evaluated script title and generated script path. */
function workflowLaunch(payload: Record<string, unknown>) {
  const result = piExtractTool(payload)?.result
  const id = pickString(result?.details, 'taskId')
  const match = /^Workflow "([\s\S]+)" started in the background\.\nTask ID: ([^\r\n]+)\n(?:Script: ([^\r\n]+)\n)?/.exec(result?.text ?? '')
  // A match always carries the title and task-id groups; only the optional script group can be absent.
  return { id, title: match && match[2] === id ? (match[1] ?? '').trim() : '', scriptPath: match && match[2] === id ? match[3] ?? '' : '' }
}

export function piWorkflowRequest(payload: Record<string, unknown>, request?: ParsedMessageContent, result?: ParsedMessageContent): AgentRequest {
  const args = piExtractTool(piPairedRequest(payload, request)?.parentObject)?.args ?? piExtractTool(payload)?.args ?? {}
  const launch = workflowLaunch(piPairedResult(payload, result)?.parentObject ?? payload)
  const metadata: NonNullable<AgentRequest['metadata']> = []
  for (const [key, label] of [['scriptPath', 'Script'], ['name', 'Saved workflow'], ['resumeFromRunId', 'Previous run']] as const) {
    const value = pickString(args, key)
    if (value)
      metadata.push({ label, value })
  }
  const argsJson = prettifyArgsJson(args.args)
  if (argsJson)
    metadata.push({ label: 'Arguments', value: argsJson })
  return {
    description: launch.title || pickString(args, 'name') || pickString(args, 'scriptPath') || 'Run workflow',
    prompt: pickString(args, 'scriptPath') ? '' : pickString(args, 'script'),
    promptLabel: 'Script',
    promptFormat: 'pre',
    metadata,
  }
}

/** Completing the launch leaves the workflow running until its custom notification arrives. */
export function piWorkflowResult(payload: Record<string, unknown>, request?: ParsedMessageContent): AgentRun {
  const tool = piExtractTool(payload)
  const launch = workflowLaunch(payload)
  const source = piWorkflowRequest(payload, request)
  const running = !!launch.id && !tool?.isError
  const metadata: AgentRun['metadata'] = []
  if (launch.id)
    metadata.push({ label: 'Task ID', value: launch.id })
  if (launch.scriptPath)
    metadata.push({ label: 'Script', value: launch.scriptPath })
  return {
    description: source.description,
    agentId: launch.id,
    // `registryKey` rides only when the launch stated a task id, never as an explicit undefined.
    ...(launch.id ? { registryKey: launch.id } : {}),
    statusLabel: running ? 'running' : 'failed',
    outcome: running ? 'running' : 'failed',
    metadata,
    body: running && launch.title ? '' : tool?.result?.text ?? '',
  }
}
