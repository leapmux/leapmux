import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { pickObject } from '../../../src/lib/jsonPick'
import { isFilesystemPath } from '../../../src/lib/paths'
import { readNativeToolOutputRecord } from '../helpers/nativeMessages'

/** Decode the original native ACP result, without a complete-text supplement. */
export function readOpenCodeNativeOutput(snapshot: NativeMessageSnapshot, callId: string): { paths: string[], previewText: string, frame: Record<string, unknown>, content: Uint8Array } {
  const record = readNativeToolOutputRecord(snapshot, {
    callId,
    spanId: callId,
    accepts: frame => frame.sessionUpdate === 'tool_call_update' && frame.toolCallId === callId && (frame.status === 'completed' || frame.status === 'failed'),
  })
  const output = pickObject(record.frame, 'rawOutput')
  const path = pickObject(output, 'metadata')?.outputPath
  if (!isFilesystemPath(path) || !/[\\/]tool-output[\\/]tool_[A-Za-z0-9]+$/u.test(path))
    throw new Error('The native OpenCode result has no exact filesystem output pointer.')
  const previewText = typeof output?.output === 'string' ? output.output : ''
  return { paths: [path], previewText, frame: record.frame, content: record.message.content }
}
