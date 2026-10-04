import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { OH_MY_PI_TRUNCATION_FIELD } from '../../../src/components/chat/providers/ohmypi/protocol'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { readNativeToolOutputRecord } from '../helpers/nativeMessages'

/** Read an opaque native artifact ID and the original Bash preview. Infer no filesystem path. */
export function ohMyPiNativeOutput(snapshot: NativeMessageSnapshot, callId: string): { artifactId: string, previewText: string, frame: Record<string, unknown>, content: Uint8Array } {
  const record = readNativeToolOutputRecord(snapshot, { callId, spanId: callId, accepts: frame => frame.type === 'tool_execution_end' && frame.toolCallId === callId && frame.toolName === 'bash' && frame.isError === false })
  const result = pickObject(record.frame, 'result')
  const details = pickObject(result, 'details')
  const truncation = pickObject(pickObject(details, OH_MY_PI_TRUNCATION_FIELD.Meta), OH_MY_PI_TRUNCATION_FIELD.Truncation)
  const artifactId = truncation?.[OH_MY_PI_TRUNCATION_FIELD.ArtifactID]
  if (typeof artifactId !== 'string' || !/^(?:0|[1-9]\d*)$/u.test(artifactId) || !Array.isArray(result?.content))
    throw new Error('The native Oh My Pi preview requires its original opaque ID and text blocks.')
  const texts = result.content.filter(isObject).filter(block => block.type === 'text').map(block => block.text)
  if (texts.some(text => typeof text !== 'string'))
    throw new Error('The native Oh My Pi preview contains an invalid text block.')
  const lines = texts.filter((text): text is string => typeof text === 'string').join('\n\n').replace(/\r\n/gu, '\n').split('\n')
  while (lines.length) {
    const last = lines.at(-1)
    if (last?.trim() === '' || /^Wall time: [\d.]+ seconds$/u.test(last ?? '') || /^Command exited with code -?\d+$/u.test(last ?? '')) {
      lines.pop()
      continue
    }
    break
  }
  return { artifactId, previewText: lines.join('\n'), frame: record.frame, content: record.message.content }
}
