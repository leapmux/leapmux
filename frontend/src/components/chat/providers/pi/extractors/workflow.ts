import type { AgentRequestSource } from '../../../results/AgentRequestMessage'
import type { AgentResultSource } from '../../../results/agentResult'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { PI_TOOL } from '~/generated/contracts/pi-protocol'
import { prettifyArgsJson } from '~/lib/jsonFormat'
import { pickString } from '~/lib/jsonPick'
import { piExtractTool, piPairedRequest, piPairedResult } from './toolCommon'

/** The launch acknowledgement supplies the evaluated script title and generated script path. */
function workflowLaunch(payload: Record<string, unknown>) {
  const result = piExtractTool(payload)?.result
  const id = pickString(result?.details, 'taskId')
  const match = /^Workflow "([\s\S]+)" started in the background\.\nTask ID: ([^\r\n]+)\n(?:Script: ([^\r\n]+)\n)?/.exec(result?.text ?? '')
  return { id, title: match && match[2] === id ? match[1].trim() : '', scriptPath: match && match[2] === id ? match[3] ?? '' : '' }
}

export function piWorkflowRequest(payload: Record<string, unknown>, request?: ParsedMessageContent, result?: ParsedMessageContent): AgentRequestSource {
  const args = piExtractTool(piPairedRequest(payload, request)?.parentObject)?.args ?? piExtractTool(payload)?.args ?? {}
  const launch = workflowLaunch(piPairedResult(payload, result)?.parentObject ?? payload)
  const metadata: NonNullable<AgentRequestSource['metadata']> = []
  for (const [key, label] of [['scriptPath', 'Script'], ['name', 'Saved workflow'], ['resumeFromRunId', 'Previous run']]) {
    const value = pickString(args, key)
    if (value)
      metadata.push({ label, value })
  }
  const argsJson = prettifyArgsJson(args.args)
  if (argsJson)
    metadata.push({ label: 'Arguments', value: argsJson })
  return {
    toolName: PI_TOOL.SubagentWorkflow,
    description: launch.title || pickString(args, 'name') || pickString(args, 'scriptPath') || 'Run workflow',
    prompt: pickString(args, 'scriptPath') ? '' : pickString(args, 'script'),
    promptLabel: 'Script',
    promptFormat: 'pre',
    metadata,
  }
}

/** Completing the launch leaves the workflow running until its custom notification arrives. */
export function piWorkflowResult(payload: Record<string, unknown>, request?: ParsedMessageContent): AgentResultSource {
  const tool = piExtractTool(payload)
  const launch = workflowLaunch(payload)
  const source = piWorkflowRequest(payload, request)
  const running = !!launch.id && !tool?.isError
  const metadata: AgentResultSource['metadata'] = []
  if (launch.id)
    metadata.push({ label: 'Task ID', value: launch.id })
  if (launch.scriptPath)
    metadata.push({ label: 'Script', value: launch.scriptPath })
  return {
    description: source.description,
    agentId: launch.id,
    registryKey: launch.id || undefined,
    status: running ? 'running' : 'failed',
    outcome: running ? 'running' : 'failed',
    metadata,
    body: running && launch.title ? '' : tool?.result?.text ?? '',
  }
}
