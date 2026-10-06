import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { isObject, pickObject, pickString } from '../../../src/lib/jsonPick'
import { readNativeToolOutputRecord } from '../helpers/nativeMessages'
import { commandCodeToolCompleted } from './toolCompleted'

export interface CommandCodeNativeImage {
  mediaType: string
  /** The base64 bytes that the native result states, with no data URL prefix. */
  data: string
}

/**
 * Read the image that the native `read_file` tool attached to its completed result.
 *
 * Command Code compresses the file image before it attaches the image, so these bytes differ from
 * the bytes of the file. The native result states the image that the model receives and that the
 * browser draws.
 */
export function readCommandCodeNativeImage(snapshot: NativeMessageSnapshot, callId: string): CommandCodeNativeImage {
  const record = readNativeToolOutputRecord(snapshot, {
    callId,
    spanId: callId,
    accepts: commandCodeToolCompleted('read_file', callId),
  })
  const result = pickObject(record.frame, 'event')?.result
  const images = Array.isArray(result) ? result.filter(isObject).filter(block => block.type === 'image') : []
  if (images.length !== 1)
    throw new Error('The native Command Code result requires exactly one image block.')
  const source = pickObject(images[0], 'source')
  const mediaType = pickString(source, 'media_type')
  const data = pickString(source, 'data')
  if (source?.type !== 'base64' || !mediaType.startsWith('image/') || !data)
    throw new Error('The native Command Code image block requires a base64 image source.')
  return { mediaType, data }
}
