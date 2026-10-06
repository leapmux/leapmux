import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { NativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { pickObject } from '../../../src/lib/jsonPick'
import { isFilesystemPath } from '../../../src/lib/paths'
import { readNativeToolOutputRecord } from '../helpers/nativeMessages'

/** Decode the original native ACP result, without a complete-text supplement. */
export function readKiroNativeOutput(snapshot: NativeMessageSnapshot, callId: string): NativeOutputReceipt {
  const record = readNativeToolOutputRecord(snapshot, {
    callId,
    spanId: callId,
    accepts: frame => frame.sessionUpdate === 'tool_call_update' && frame.toolCallId === callId && (frame.status === 'completed' || frame.status === 'failed'),
  })
  const output = pickObject(record.frame, 'rawOutput')
  const transformation = pickObject(pickObject(pickObject(record.frame, '_meta'), 'kiro'), 'outputTransformation')
  const path = transformation?.absFilePath
  if (transformation?.kind !== 'offloaded' || !isFilesystemPath(path) || !/[\\/]tool-outputs[\\/][\w-]+-[a-f0-9]{8}\.txt$/iu.test(path))
    throw new Error('The native Kiro result has no exact filesystem output pointer.')
  if (typeof output?.output !== 'string')
    throw new Error('The native Kiro result has no original preview string.')
  return { paths: [path], previewText: output.output, frame: record.frame, content: record.message.content }
}
