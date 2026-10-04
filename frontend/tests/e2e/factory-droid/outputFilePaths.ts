import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { isFilesystemPath } from '../../../src/lib/paths'
import { readNativeToolOutputRecord } from '../helpers/nativeMessages'

/** Decode the flat native notification and its independent terminal UUID path. */
export function readDroidNativeOutput(snapshot: NativeMessageSnapshot, callId: string): { paths: string[], previewText: string, frame: Record<string, unknown>, content: Uint8Array } {
  const record = readNativeToolOutputRecord(snapshot, { callId, spanId: `droid-tool-${callId}`, accepts: frame => frame.type === 'tool_result' && frame.toolUseId === callId })
  if (typeof record.frame.content !== 'string' || typeof record.frame.isError !== 'boolean')
    throw new Error('The native Droid result has no exact text or failure fields.')
  const pointers = [...record.frame.content.matchAll(/(?:^|\r?\n)Full command output saved to: ([^\r\n]+) \([^\r\n]+\)(?=\r?\n|$)/gu)]
  const path = pointers.length === 1 ? pointers[0]?.[1] : undefined
  if (!isFilesystemPath(path) || !/[\\/]droid-terminal-[\w-]+[\\/][0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}\.log$/iu.test(path))
    throw new Error('The native Droid result has no unique terminal output path.')
  return { paths: [path], previewText: record.frame.content, frame: record.frame, content: record.message.content }
}
