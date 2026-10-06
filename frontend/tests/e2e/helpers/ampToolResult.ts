import type { MockModelRequestRecord } from './mockModelScript'
import type { ManagedNativeScenarioContext, NativeToolOutcome, NativeToolResultReader } from './nativeScenario'
import { isObject } from '../../../src/lib/jsonPick'
import { ampToolUseID } from './ampSurface'
import { currentNativeAgent } from './nativeScenario'
import { nativeToolResultContent } from './nativeToolResult'

/** Read one actual executor run from the recorded Amp model request. */
export function ampToolResult(request: MockModelRequestRecord, nativeCallId: string): NativeToolOutcome {
  let content: unknown
  try {
    content = nativeToolResultContent(request, nativeCallId)
  }
  catch (cause) {
    throw new Error(`Amp returned no unique executor run for ${nativeCallId}.`, { cause })
  }
  if (typeof content !== 'string')
    throw new Error(`Amp returned no unique executor run for ${nativeCallId}.`)
  let run: unknown
  try {
    run = JSON.parse(content)
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
