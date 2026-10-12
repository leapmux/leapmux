import { MUSE_LOG_EVENT, MUSE_STREAM_KIND, MUSE_SUPPLEMENT_FIELD } from '~/generated/contracts/muse-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { museItem, museParams } from './protocol'

export interface MuseNativeResult {
  text: string
  structured: Record<string, unknown> | undefined
}

/** Validate both unchanged native records against the item's origin and batch. */
export function museNativeResult(payload: unknown, supplemental: unknown): MuseNativeResult | undefined {
  const item = museItem(payload)
  const params = museParams(payload)
  const sourceRange = pickObject(params, 'sourceRange')
  const source = pickObject(sourceRange, 'first')
  const sourceStream = pickObject(sourceRange, 'stream')
  if (!item || !params || !source || sourceStream?.kind !== MUSE_STREAM_KIND.Session || sourceStream.id !== params.sessionId
    || typeof params.sessionId !== 'string' || params.sessionId.trim() === ''
    || typeof item.turnId !== 'string' || item.turnId.trim() === ''
    || typeof item.callId !== 'string' || item.callId.trim() === ''
    || typeof item.tool !== 'string' || item.tool.trim() === ''
    || typeof item.args !== 'string'
    || !isObject(supplemental) || supplemental[MUSE_SUPPLEMENT_FIELD.Unavailable] !== undefined) {
    return undefined
  }
  const raw = supplemental[MUSE_SUPPLEMENT_FIELD.NativeRecords]
  if (!Array.isArray(raw) || raw.some(record => !isObject(record)))
    return undefined
  const records = raw.filter(isObject)
  const unique = new Map<string, Record<string, unknown>>()
  const sequences = new Map<number, string>()
  for (const record of records) {
    const id = pickString(record, 'id')
    const sequence = record.sequence
    const stream = pickObject(record, 'stream')
    const body = pickObject(record, 'payload')
    if (!id || !Number.isSafeInteger(sequence) || typeof sequence !== 'number' || sequence < 1
      || stream?.kind !== MUSE_STREAM_KIND.Session || stream.id !== params.sessionId || body?.kind !== 'run' || body.run_id !== item.turnId) {
      return undefined
    }
    const previous = unique.get(id)
    if ((previous && JSON.stringify(previous) !== JSON.stringify(record))
      || (sequences.has(sequence) && sequences.get(sequence) !== id)) {
      return undefined
    }
    unique.set(id, record)
    sequences.set(sequence, id)
  }
  const origin = unique.get(pickString(source, 'id'))
  if (origin?.sequence !== source.sequence
    || pickObject(origin, 'stream')?.kind !== sourceStream.kind
    || pickObject(origin, 'stream')?.id !== sourceStream.id) {
    return undefined
  }
  const event = pickObject(pickObject(origin, 'payload'), 'event')
  if (event?.kind !== MUSE_LOG_EVENT.AssistantToolCallsCommitted || typeof event.message_id !== 'string' || event.message_id.trim() === '' || !Array.isArray(event.tool_calls))
    return undefined
  if (event.tool_calls.some(call => !isObject(call) || typeof call.call_id !== 'string' || call.call_id.trim() === ''
    || typeof call.name !== 'string' || call.name.trim() === '' || typeof call.args !== 'string')) {
    return undefined
  }
  const calls = event.tool_calls.filter(isObject)
  const matches = calls.map((call, index) => ({ call, index })).filter(({ call }) => call.call_id === item.callId)
  const match = matches[0]
  if (matches.length !== 1 || !match || match.call.name !== item.tool || match.call.args !== item.args)
    return undefined
  // SourceRange.Last ends the item's fold. The model result batch can follow it.
  const batches = [...unique.values()].filter((record) => {
    const result = pickObject(pickObject(record, 'payload'), 'event')
    return result?.kind === MUSE_LOG_EVENT.ToolResultBatchCommitted && result.batch_id === event.message_id
  })
  if (batches.length !== 1)
    return undefined
  const batch = pickObject(pickObject(batches[0], 'payload'), 'event')
  if (!Array.isArray(batch?.results))
    return undefined
  const entries: Record<string, unknown>[] = []
  for (const result of batch.results) {
    if (!isObject(result) || typeof result.tool_call_index !== 'number' || !Number.isSafeInteger(result.tool_call_index)
      || result.tool_call_index < 0 || typeof result.tool_call_id !== 'string' || result.tool_call_id.trim() === ''
      || typeof result.text !== 'string') {
      return undefined
    }
    entries.push(result)
  }
  const results = entries.filter(result => result.tool_call_id === item.callId && result.tool_call_index === match.index)
  if (results.length !== 1 || typeof results[0]?.text !== 'string')
    return undefined
  const text = results[0].text
  let structured: Record<string, unknown> | undefined
  try {
    const parsed: unknown = JSON.parse(text)
    structured = isObject(parsed) ? parsed : undefined
  }
  catch {
    structured = undefined
  }
  return { text, structured }
}
