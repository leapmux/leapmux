import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { NativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { isFilesystemPath } from '../../../src/lib/paths'
import { readNativeToolOutputRecord } from '../helpers/nativeMessages'
import { qoderNativeToolResult } from './nativeToolResult'

/** Read the original native result and its session-owned persistence metadata. */
export function readQoderNativeOutput(snapshot: NativeMessageSnapshot, callId: string): NativeOutputReceipt {
  const record = readNativeToolOutputRecord(snapshot, {
    callId,
    spanId: callId,
    accepts: (frame) => {
      const content = pickObject(frame, 'message')?.content
      return frame.type === 'user' && frame.session_id === snapshot.agentSessionId && Array.isArray(content)
        && content.some(block => isObject(block) && block.type === 'tool_result' && block.tool_use_id === callId)
    },
  })
  const blocks = pickObject(record.frame, 'message')?.content
  const matching = Array.isArray(blocks) ? blocks.filter(isObject).filter(block => block.type === 'tool_result' && block.tool_use_id === callId) : []
  const block = matching.length === 1 ? matching[0] : undefined
  const result = pickObject(record.frame, 'tool_use_result')
  const path = pickObject(result, 'persistedOutput')?.path
  const pieces = typeof path === 'string' ? path.split(/[\\/]/u) : []
  if (!block || block.is_error === true || !isFilesystemPath(path) || pieces.at(-2) !== `session-${snapshot.agentSessionId}`
    || pieces.at(-3) !== 'tool-outputs' || !/^[\w-]+\.output$/u.test(pieces.at(-1) ?? '')) {
    throw new Error('The native Qoder pointer belongs to another native result or session.')
  }
  const previewText = qoderNativeToolResult([record.frame], callId).text
  return { paths: [path], previewText, frame: record.frame, content: record.message.content }
}
