import type { MockModelRequestRecord } from './mockModelScript'
import type { ManagedNativeScenarioContext, NativeToolOutcome, NativeToolResultReader } from './nativeScenario'
import { isObject } from '../../../src/lib/jsonPick'
import { ampToolUseID } from './ampSurface'
import { currentNativeAgent } from './nativeScenario'

/** Read one actual executor run from the recorded Amp model request. */
export function ampToolResult(request: MockModelRequestRecord, nativeCallId: string): NativeToolOutcome {
  const body = isObject(request.body) ? request.body : undefined
  const messages = Array.isArray(body?.messages) ? body.messages.filter(isObject) : []
  const matches = messages.flatMap(message => Array.isArray(message.content) ? message.content.filter(isObject) : [])
    .filter(block => block.type === 'tool_result' && block.tool_use_id === nativeCallId)
  if (matches.length !== 1 || typeof matches[0]?.content !== 'string')
    throw new Error(`Amp returned no unique executor run for ${nativeCallId}.`)
  let run: unknown
  try {
    run = JSON.parse(matches[0].content)
  }
  catch {
    throw new Error(`Amp executor run ${nativeCallId} contains invalid JSON.`)
  }
  if (!isObject(run))
    throw new Error(`Amp executor run ${nativeCallId} contains no result.`)
  const raw = run.result ?? run.error ?? run.reason
  if (raw === undefined || raw === null)
    throw new Error(`Amp executor run ${nativeCallId} contains no result.`)
  const text = typeof raw === 'string' ? raw : JSON.stringify(raw)
  let value: unknown = raw
  if (typeof raw === 'string') {
    try {
      value = JSON.parse(raw)
    }
    catch {
      value = undefined
    }
  }
  const code = isObject(value) ? value.exitCode : undefined
  if (code !== undefined && (typeof code !== 'number' || !Number.isSafeInteger(code)))
    throw new Error('Amp returned an invalid native exit code.')
  return {
    text,
    ...(run.status === 'error' ? { failed: true } : {}),
    ...(typeof code === 'number' ? { exitCode: code } : {}),
  }
}

/** Resolve scripted IDs through the real Amp thread that accepted their calls. */
export function ampToolResultReader(context: ManagedNativeScenarioContext): NativeToolResultReader {
  return async (request, callId) => {
    const agent = await currentNativeAgent(context)
    if (!agent.agentSessionId)
      throw new Error('Amp has no native thread for the tool result.')
    return ampToolResult(request, ampToolUseID(agent.agentSessionId, callId))
  }
}
