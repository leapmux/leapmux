import { ZCODE_EVENT } from '../../../src/generated/contracts/zcode-protocol'
import { isObject } from '../../../src/lib/jsonPick'

export interface ZCodeWorkflowLaunch {
  runId: string
}

export interface ZCodeWorkflowCompletion {
  runId: string
  status: 'completed' | 'failed'
  text: string
}

/** Read the current native launch formatter. A compile check is not a launch. */
export function zcodeWorkflowLaunch(text: string): ZCodeWorkflowLaunch {
  const match = /^The workflow script compiled cleanly and the run started in the background with ID: (dwfrun-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\./u.exec(text)
  const runId = match?.[1]
  if (!runId)
    throw new Error('The native ZCode result contains no actual workflow launch.')
  return { runId }
}

/** Decode the native XML text once. Encoded tags must remain text. */
function decodeXml(text: string): string {
  const entities: Readonly<Record<string, string>> = { '&quot;': '"', '&apos;': '\'', '&lt;': '<', '&gt;': '>', '&amp;': '&' }
  return text.replace(/&(?:quot|apos|lt|gt|amp);/gu, value => entities[value] ?? value)
}

function tag(text: string, name: 'run_id' | 'status' | 'owned_by_this_session' | 'result' | 'error'): string | undefined {
  const opening = name === 'error' ? `<${name} code="[^"]*">` : `<${name}>`
  const matches = Array.from(text.matchAll(new RegExp(`${opening}([\\s\\S]*?)<\\/${name}>`, 'gu')))
  if (matches.length > 1)
    throw new Error(`The native ZCode workflow repeats its ${name} field.`)
  const value = matches[0]?.[1]
  return value === undefined ? undefined : name === 'result' ? value : decodeXml(value)
}

/** Require the exact owned run's final result, including an empty native value. */
export function zcodeWorkflowCompletion(text: string, runId: string): ZCodeWorkflowCompletion {
  if (!runId.trim() || tag(text, 'run_id') !== runId || tag(text, 'owned_by_this_session') !== 'true')
    throw new Error('The native ZCode result belongs to another run or session.')
  const status = tag(text, 'status')
  if (status !== 'completed' && status !== 'errored')
    throw new Error('The native ZCode script has no completed result or execution error.')
  const result = tag(text, 'result')
  const error = tag(text, 'error')
  if (status === 'completed') {
    if (result === undefined || error !== undefined || !result.startsWith('\n') || !result.endsWith('\n'))
      throw new Error('The native ZCode script has no exact final output.')
    return { runId, status: 'completed', text: result.slice(1, -1) }
  }
  if (error === undefined)
    throw new Error('The native ZCode script has no exact execution error.')
  return { runId, status: 'failed', text: error }
}

/** Read one persisted native GetWorkflowRun card with its session and call identity. */
export function zcodeStoredWorkflowCompletion(frames: readonly unknown[], sessionId: string, callId: string, runId: string): ZCodeWorkflowCompletion {
  if (!sessionId.trim() || !callId.trim())
    throw new Error('The native ZCode read requires the exact session and call ID.')
  const results = frames.filter(isObject).filter(frame => frame.type === ZCODE_EVENT.ToolUpdated
    && isObject(frame.payload) && frame.payload.kind === 'result' && frame.payload.toolCallId === callId)
  if (results.length !== 1)
    throw new Error('The native ZCode read requires one exact persisted result.')
  const frame = results[0]
  const result = frame && isObject(frame.payload) ? frame.payload.result : undefined
  if (!frame || frame.sessionId !== sessionId || !isObject(result) || result.success === false
    || result.truncated === true || typeof result.content !== 'string') {
    throw new Error('The native ZCode read is incomplete or belongs to another session.')
  }
  return zcodeWorkflowCompletion(result.content, runId)
}
