import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeToolOutcome } from '../helpers/nativeScenario'
import { DROID_TOOL } from '../../../src/generated/contracts/droid-protocol'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeToolResult } from '../helpers/nativeToolResult'

function matchesOriginalId(nativeId: string, originalId: string): boolean {
  if (originalId.startsWith('call_'))
    return nativeId === originalId
  if (nativeId === `call_${originalId}`)
    return true
  // The native mapper removes toolu_, clips the original ID, then adds its cached counter.
  const clipped = originalId.replace(/^toolu_/, '').slice(0, 24)
  const prefix = `call_${clipped}_`
  return nativeId.startsWith(prefix) && /^(?:0|[1-9]\d*)$/.test(nativeId.slice(prefix.length))
}

function droidNativeCall(request: MockModelRequestRecord | undefined, callId: string, toolName: string): { id: string, arguments: string } {
  if (!callId || request?.protocol !== 'openai-chat-completions' || !isObject(request.body) || !Array.isArray(request.body.messages))
    throw new Error('The native Droid result requires an exact call ID and actual Chat model messages.')
  const calls = request.body.messages.filter(isObject).filter(message => message.role === 'assistant').flatMap(message => Array.isArray(message.tool_calls) ? message.tool_calls.filter(isObject) : []).filter(call => typeof call.id === 'string' && matchesOriginalId(call.id, callId))
  if (calls.length !== 1)
    throw new Error('The original Droid call must identify one actual native assistant call.')
  const call = calls[0]
  const fn = call && isObject(call.function) ? call.function : null
  if (!call || typeof call.id !== 'string' || fn?.name !== toolName || typeof fn.arguments !== 'string')
    throw new Error('The exact Droid assistant call does not match the requested native tool.')
  return { id: call.id, arguments: fn.arguments }
}

/** Read the final native Script snapshot. Printed output cannot supply its status. */
function droidScriptResult(text: string, nativeId: string): NativeToolOutcome {
  const decode = (value: string): unknown => JSON.parse(value.startsWith('Error: ') ? value.slice('Error: '.length) : value)
  let snapshotText = text
  let snapshot: unknown = decode(snapshotText)
  if (Array.isArray(snapshot)) {
    const final = snapshot.at(-1)
    if (!isObject(final) || final.type !== 'text' || typeof final.text !== 'string')
      throw new Error('The native Droid Script result lacks its final snapshot text block.')
    snapshotText = final.text
    snapshot = decode(snapshotText)
  }
  if (!isObject(snapshot) || (snapshot.toolCallId !== undefined && snapshot.toolCallId !== nativeId))
    throw new Error('The native Droid Script snapshot contains invalid data or a mismatched call ID.')
  // Native history removes toolCallId after it validates the original snapshot against the assistant call.
  if (snapshot.status === 'completed' && Object.hasOwn(snapshot, 'result') && !snapshotText.startsWith('Error: '))
    return { text, failed: false }
  if (snapshot.status === 'failed' && typeof snapshot.error === 'string')
    return { text, failed: true }
  throw new Error('The native Droid Script result lacks a completed inline result or failed error.')
}

/** Correlate the original scripted call with its exact native assistant and result IDs. */
export function readDroidToolResult(request: MockModelRequestRecord, callId: string, toolName: string = DROID_TOOL.Execute): NativeToolOutcome {
  const call = droidNativeCall(request, callId, toolName)
  const args: unknown = JSON.parse(call.arguments)
  if (!isObject(args) || (toolName === DROID_TOOL.Execute && typeof args.command !== 'string')
    || (toolName === DROID_TOOL.Read && typeof args.file_path !== 'string')) {
    throw new Error('The exact native Droid call contains malformed tool arguments.')
  }
  if (toolName === 'Script' && (typeof args.script !== 'string' || !args.script.trim()
    || new TextEncoder().encode(args.script).byteLength > 512 * 1024
    || (args.waitForMs !== undefined && (typeof args.waitForMs !== 'number' || !Number.isFinite(args.waitForMs) || args.waitForMs < 0)))) {
    throw new Error('The exact native Droid Script call contains malformed source or observation arguments.')
  }
  const text = nativeToolResult(request, call.id)
  if (toolName === 'Script')
    return droidScriptResult(text, call.id)
  if (toolName !== DROID_TOOL.Execute)
    return { text }
  const trailer = /(?:^|\r?\n)\[Process exited with code (-?\d+)\]\s*$/.exec(text)
  const exitCode = trailer ? Number(trailer[1]) : undefined
  if (exitCode === undefined || !Number.isSafeInteger(exitCode))
    throw new Error('The native Droid Execute result contains no complete integer exit trailer.')
  return { text, exitCode, failed: exitCode !== 0 }
}

export function nativeDroidCallId(request: MockModelRequestRecord | undefined, toolName: string, originalCallId: string): string {
  return droidNativeCall(request, originalCallId, toolName).id
}
