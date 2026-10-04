import { LETTA_DELTA_FIELD, LETTA_DELTA_KIND, LETTA_TOOL_OUTPUT } from '~/generated/contracts/letta-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

export type LettaReturnedData
  = | { kind: 'present', value: unknown, status: string | undefined }
    | { kind: 'absent' | 'invalid' }

/** Read the stored native payload or its raw frame wrapper. */
export function lettaToolPayload(payload: unknown): Record<string, unknown> | null {
  if (!isObject(payload))
    return null
  const nested = pickObject(payload, 'payload')
  return nested && Object.keys(nested).length > 0 ? nested : payload
}

/** Recognize only the exact native ID for this call's current output window. */
export function isLettaToolProgress(source: Record<string, unknown> | null): boolean {
  const callId = pickString(source, LETTA_DELTA_FIELD.ToolCallID)
  return pickString(source, LETTA_DELTA_FIELD.MessageType) === LETTA_DELTA_KIND.ToolReturnMessage
    && !!callId && pickString(source, LETTA_DELTA_FIELD.ID) === LETTA_TOOL_OUTPUT.StreamIDPrefix + callId
}

/** Select present returned data without replacing empty, zero, false, or null values. */
export function lettaReturnedData(source: Record<string, unknown> | null): LettaReturnedData {
  if (!source)
    return { kind: 'absent' }
  const topPresent = Object.hasOwn(source, LETTA_DELTA_FIELD.ToolReturn)
  const top = source[LETTA_DELTA_FIELD.ToolReturn]
  const status = pickString(source, LETTA_DELTA_FIELD.Status, undefined)
  if (!Object.hasOwn(source, LETTA_DELTA_FIELD.ToolReturns))
    return topPresent && top !== undefined ? { kind: 'present', value: top, status } : { kind: 'absent' }
  const returns = source[LETTA_DELTA_FIELD.ToolReturns]
  const callId = pickString(source, LETTA_DELTA_FIELD.ToolCallID)
  if (!Array.isArray(returns) || !callId)
    return { kind: 'invalid' }
  const matching = returns.filter(isObject).filter(result => pickString(result, LETTA_DELTA_FIELD.ToolCallID) === callId)
  const selected = matching.length === 1 ? matching[0] : undefined
  if (!selected)
    return { kind: 'invalid' }
  const selectedStatus = pickString(selected, LETTA_DELTA_FIELD.Status, undefined)
  if (status !== undefined && selectedStatus !== undefined && status !== selectedStatus)
    return { kind: 'invalid' }
  const selectedPresent = Object.hasOwn(selected, LETTA_DELTA_FIELD.ToolReturn)
  const value = selected[LETTA_DELTA_FIELD.ToolReturn]
  if (topPresent) {
    if (top === undefined || (selectedPresent && !sameLettaReturn(top, value)))
      return { kind: 'invalid' }
    return { kind: 'present', value: top, status: status ?? selectedStatus }
  }
  return selectedPresent && value !== undefined
    ? { kind: 'present', value, status: status ?? selectedStatus }
    : { kind: 'absent' }
}

function sameLettaReturn(left: unknown, right: unknown): boolean {
  const pending: [unknown, unknown][] = [[left, right]]
  for (;;) {
    const pair = pending.pop()
    if (!pair)
      return true
    const [first, second] = pair
    if (Object.is(first, second))
      continue
    if (Array.isArray(first) || Array.isArray(second)) {
      if (!Array.isArray(first) || !Array.isArray(second) || first.length !== second.length)
        return false
      for (let index = 0; index < first.length; index++)
        pending.push([first[index], second[index]])
      continue
    }
    if (!isObject(first) || !isObject(second))
      return false
    const keys = Object.keys(first)
    if (keys.length !== Object.keys(second).length)
      return false
    for (const key of keys) {
      if (!Object.hasOwn(second, key))
        return false
      pending.push([first[key], second[key]])
    }
  }
}
