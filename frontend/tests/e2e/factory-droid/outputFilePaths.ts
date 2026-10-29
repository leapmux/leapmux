import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { NativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { droidNativeOutputFilePaths } from '../../../src/components/chat/providers/droid/extractors/outputFilePaths'
import { readNativeToolOutputRecord } from '../helpers/nativeMessages'

/** Decode the flat native notification and its independent terminal UUID path. */
export function readDroidNativeOutput(snapshot: NativeMessageSnapshot, callId: string): NativeOutputReceipt {
  const record = readNativeToolOutputRecord(snapshot, {
    callId,
    spanId: `droid-tool-${callId}`,
    accepts: frame => frame.type === 'tool_result' && frame.toolUseId === callId,
  })
  if (typeof record.frame.content !== 'string' || typeof record.frame.isError !== 'boolean')
    throw new TypeError('The native Droid result has no exact text or failure fields.')
  const paths = droidNativeOutputFilePaths(record.frame.content)
  const path = paths.length === 1
    ? paths[0]
    : undefined
  if (path === undefined)
    throw new Error('The native Droid result has no unique terminal output path.')
  const content = record.frame.content
  const trailer = /\[Process exited with code (-?\d+)\]\s*$/u.exec(content)
  const exitCode = trailer
    ? Number(trailer[1])
    : Number.NaN
  let previewText = trailer && Number.isSafeInteger(exitCode)
    ? content.slice(0, trailer.index).replace(/\r?\n\r?\n$/u, '')
    : content
  const failure = `Error: Command failed (exit code: ${exitCode})\n`
  if (exitCode !== 0 && previewText.startsWith(failure))
    previewText = previewText.slice(failure.length)
  return {
    paths: [path],
    previewText,
    frame: record.frame,
    content: record.message.content,
  }
}
