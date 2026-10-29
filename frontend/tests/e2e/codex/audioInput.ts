import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { Buffer } from 'node:buffer'
import { isObject } from '../../../src/lib/jsonPick'

/** Read only typed audio from the current native user turn, excluding earlier history. */
export function codexAudioInputs(request: Pick<MockModelRequestRecord, 'protocol' | 'body'>): { mimeType: string, bytes: Buffer }[] {
  if (request.protocol !== 'openai-responses')
    throw new Error('The Codex audio proof requires the Responses API.')
  if (!isObject(request.body) || !Array.isArray(request.body.input))
    return []
  const result: { mimeType: string, bytes: Buffer }[] = []
  for (let index = request.body.input.length - 1; index >= 0; index--) {
    const row: unknown = request.body.input[index]
    if (isObject(row) && (row.role === 'system' || row.role === 'developer'))
      continue
    if (!isObject(row) || row.role !== 'user')
      break
    if (!Array.isArray(row.content))
      continue
    for (const part of row.content.filter(isObject)) {
      if (part.type !== 'input_audio')
        continue
      const match = typeof part.audio_url === 'string'
        ? /^data:([^;,]+);base64,([A-Za-z0-9+/]*={0,2})$/u.exec(part.audio_url)
        : null
      if (!match)
        throw new Error('The native audio item has no canonical base64 data URI.')
      const bytes = Buffer.from(match[2]!, 'base64')
      if (bytes.length === 0 || bytes.toString('base64') !== match[2])
        throw new Error('The native audio item has no nonempty canonical payload.')
      result.push({ mimeType: match[1]!, bytes })
    }
  }
  return result
}
