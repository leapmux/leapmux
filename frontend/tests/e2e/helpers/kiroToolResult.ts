import type { MockModelRequestRecord } from './mockModelScript'
import type { NativeToolOutcome } from './nativeScenario'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeToolResultEntry } from './nativeToolResult'

/** Read only one Kiro service tool result and its observed status. */
export function kiroToolResult(request: MockModelRequestRecord, callId: string): NativeToolOutcome {
  if (request.protocol !== 'aws-event-stream')
    throw new Error(`The Kiro result reader requires a Kiro service request, not ${request.protocol}.`)
  let result: Record<string, unknown>
  try {
    result = nativeToolResultEntry(request, callId)
  }
  catch (cause) {
    throw new Error(`Kiro returned no unique native result for ${callId}.`, { cause })
  }
  if (!Array.isArray(result.content) || result.content.length === 0)
    throw new Error('Kiro returned no native tool content.')
  const codes: number[] = []
  const text = result.content.map((block: unknown) => {
    if (!isObject(block))
      throw new Error('Kiro returned an invalid native content block.')
    if (typeof block.text === 'string')
      return block.text
    if ('json' in block) {
      if (isObject(block.json) && typeof block.json.exitCode === 'number' && Number.isSafeInteger(block.json.exitCode))
        codes.push(block.json.exitCode)
      return JSON.stringify(block.json)
    }
    throw new Error('Kiro returned no text or JSON for its native result.')
  }).join('\n')
  const printed = [...text.matchAll(/^Exit Code:\s*(-?\d+)\s*$/gm)].map(match => Number(match[1]))
  const unique = [...new Set([...codes, ...printed])]
  if (unique.length > 1)
    throw new Error('Kiro returned conflicting native exit codes.')
  return {
    text,
    ...(result.status === 'error' ? { failed: true } : result.status === 'success' ? { failed: false } : {}),
    ...(unique[0] !== undefined ? { exitCode: unique[0] } : {}),
  }
}
