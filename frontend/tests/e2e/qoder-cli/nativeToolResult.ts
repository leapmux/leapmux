import { isObject } from '../../../src/lib/jsonPick'

/** Read the actual native result without assuming that its Worker span equals the call ID. */
export function qoderNativeToolResult(frames: readonly unknown[], callId: string): { original: Record<string, unknown>, text: string } {
  if (!callId)
    throw new Error('The native Qoder tool result requires its exact call ID.')
  const results = frames.filter(isObject).filter(frame => frame.type === 'user').flatMap((frame) => {
    const message = isObject(frame.message) ? frame.message : undefined
    const content = Array.isArray(message?.content) ? message.content : []
    return content.filter(isObject).filter(block => block.type === 'tool_result' && block.tool_use_id === callId)
  })
  const original = results.length === 1 ? results[0] : undefined
  if (!original || original.is_error === true)
    throw new Error('The native Qoder tool result requires one exact successful tool result.')
  if (typeof original.content === 'string')
    return { original, text: original.content }
  if (!Array.isArray(original.content) || original.content.length === 0)
    throw new Error('The native Qoder result has no text content.')
  const texts: string[] = []
  for (const block of original.content) {
    if (!isObject(block) || block.type !== 'text' || typeof block.text !== 'string')
      throw new Error('The native Qoder text result contains another content type.')
    texts.push(block.text)
  }
  return { original, text: texts.join('') }
}
