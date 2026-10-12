import { isObject, pickString } from '~/lib/jsonPick'

/** The call identity shared by a stored native request and its result. */
export function storedFunctionCallID(record: Record<string, unknown>): string | undefined {
  const camel = pickString(record, 'callId', undefined)
  const snake = pickString(record, 'call_id', undefined)
  if (camel && snake && camel !== snake)
    return undefined
  return camel || snake || undefined
}

/** A streamed result updates the call; a later completed record ends it. */
export function storedFunctionIsProgress(record: Record<string, unknown>): boolean {
  return pickString(record, 'status') === 'in_progress'
}

/** Use only an explicit native failure marker for a failed result. */
export function storedFunctionFailed(record: Record<string, unknown>): boolean {
  const status = pickString(record, 'status')
  return status === 'failed' || status === 'error' || record.is_error === true
}

/** Keep arguments that the stored request supplies as JSON or an object. */
export function storedFunctionArgs(raw: unknown): Record<string, unknown> {
  if (isObject(raw))
    return raw
  if (typeof raw !== 'string')
    return {}
  try {
    const parsed: unknown = JSON.parse(raw)
    return isObject(parsed) ? parsed : { arguments: raw }
  }
  catch {
    return { arguments: raw }
  }
}

/** Draw native text directly and retain an unknown output as JSON text. */
export function storedFunctionOutputText(raw: unknown): string {
  if (typeof raw === 'string')
    return raw
  if (isObject(raw) && typeof raw.text === 'string')
    return raw.text
  if (Array.isArray(raw)) {
    return raw.map((part) => {
      if (isObject(part) && typeof part.text === 'string')
        return part.text
      return JSON.stringify(part) ?? ''
    }).join('\n')
  }
  return JSON.stringify(raw) ?? ''
}
