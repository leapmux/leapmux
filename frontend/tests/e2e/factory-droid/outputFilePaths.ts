import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { NativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { isFilesystemPath } from '../../../src/lib/paths'
import { readNativeToolOutputRecord } from '../helpers/nativeMessages'

/** Decode the flat native notification and its independent terminal UUID path. */
export function readDroidNativeOutput(snapshot: NativeMessageSnapshot, callId: string): NativeOutputReceipt {
  const record = readNativeToolOutputRecord(snapshot, { callId, spanId: `droid-tool-${callId}`, accepts: frame => frame.type === 'tool_result' && frame.toolUseId === callId })
  if (typeof record.frame.content !== 'string' || typeof record.frame.isError !== 'boolean')
    throw new Error('The native Droid result has no exact text or failure fields.')
  const pointers = [...record.frame.content.matchAll(/(?:^|\r?\n)Full command output saved to: ([^\r\n]+) \([^\r\n]+\)(?=\r?\n|$)/gu)]
  const path = pointers.length === 1 ? pointers[0]?.[1] : undefined
  if (!isFilesystemPath(path) || !/[\\/]droid-terminal-[\w-]+[\\/][0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}\.log$/iu.test(path))
    throw new Error('The native Droid result has no unique terminal output path.')
  const content = record.frame.content
  const trailer = /\[Process exited with code (-?\d+)\]\s*$/u.exec(content)
  const exitCode = trailer ? Number(trailer[1]) : Number.NaN
  let previewText = trailer && Number.isSafeInteger(exitCode) ? content.slice(0, trailer.index).replace(/\r?\n\r?\n$/u, '') : content
  const failure = `Error: Command failed (exit code: ${exitCode})\n`
  if (exitCode !== 0 && previewText.startsWith(failure))
    previewText = previewText.slice(failure.length)
  return { paths: [path], previewText, frame: record.frame, content: record.message.content }
}
