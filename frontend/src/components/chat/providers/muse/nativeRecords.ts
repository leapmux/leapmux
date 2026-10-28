import { MUSE_STREAM_KIND } from '~/generated/contracts/muse-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

/** Preserve one exact record identity for each native session position. */
export function museNativeRecordSet(raw: unknown, sessionId: string): Map<string, Record<string, unknown>> | undefined {
  if (!Array.isArray(raw) || raw.some(record => !isObject(record)))
    return undefined
  const unique = new Map<string, Record<string, unknown>>()
  const sequences = new Map<number, string>()
  for (const record of raw.filter(isObject)) {
    const id = pickString(record, 'id')
    const sequence = record.sequence
    const stream = pickObject(record, 'stream')
    if (!id || typeof sequence !== 'number' || !Number.isSafeInteger(sequence) || sequence < 1
      || stream?.kind !== MUSE_STREAM_KIND.Session || stream.id !== sessionId) {
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
  return unique
}
