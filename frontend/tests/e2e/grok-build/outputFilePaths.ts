import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { NativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { pickObject } from '../../../src/lib/jsonPick'
import { isFilesystemPath } from '../../../src/lib/paths'
import { acpClosedToolCall } from '../helpers/acpToolFrame'
import { readNativeToolOutputRecord } from '../helpers/nativeMessages'

/** Decode the original native ACP result, without a complete-text supplement. */
export function readGrokNativeOutput(snapshot: NativeMessageSnapshot, callId: string): NativeOutputReceipt {
  const record = readNativeToolOutputRecord(snapshot, {
    callId,
    spanId: callId,
    accepts: frame => acpClosedToolCall(frame, callId),
  })
  const output = pickObject(record.frame, 'rawOutput')
  const path = output?.output_file
  if (!(output?.type === 'Bash') || !isFilesystemPath(path) || !/[\\/]sessions[\\/][^\\/]+[\\/][^\\/]+[\\/]terminal[\\/][^\\/]+\.log$/u.test(path))
    throw new Error('The native Grok result has no exact filesystem output pointer.')
  if (!path.endsWith(`${callId}.log`) || path.split(/[\\/]/u).at(-3) !== snapshot.agentSessionId)
    throw new Error('The native Grok path belongs to another call or session.')
  const bytes = output.output
  if (!Array.isArray(bytes) || bytes.some(value => !Number.isInteger(value) || value < 0 || value > 255))
    throw new Error('The native Grok result has invalid preview bytes.')
  const previewText = new TextDecoder('utf-8', { fatal: true }).decode(new Uint8Array(bytes))
  return { paths: [path], previewText, frame: record.frame, content: record.message.content }
}
