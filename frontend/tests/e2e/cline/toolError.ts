import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeToolOutcome } from '../helpers/nativeScenario'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeToolResult } from '../helpers/nativeToolResult'

/**
 * Read the error of one failed native tool call from the model request that follows it.
 *
 * Cline's agent runtime turns a tool that throws into the output `{ error: <message> }`
 * (`sdk/packages/agents/src/agent-runtime.ts`), and the model reads that object as JSON.
 * A substring search of the JSON would see the message with its quotes escaped, so this
 * decodes it and returns the message alone.
 */
export function clineToolError(request: MockModelRequestRecord, callId: string): NativeToolOutcome {
  const content = nativeToolResult(request, callId)
  let value: unknown
  try {
    value = JSON.parse(content)
  }
  catch {
    throw new Error(`The Cline tool result for ${callId} is not a JSON tool output.`)
  }
  if (!isObject(value) || Object.keys(value).length !== 1 || typeof value.error !== 'string' || value.error === '')
    throw new Error(`The Cline tool result for ${callId} states no tool error.`)
  return { text: value.error }
}
