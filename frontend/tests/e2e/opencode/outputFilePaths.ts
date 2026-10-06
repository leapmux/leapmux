import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { ComputedNativeToolOutput } from '../helpers/nativeToolOutput'
import type { NativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { pickObject } from '../../../src/lib/jsonPick'
import { isFilesystemPath } from '../../../src/lib/paths'
import { readNativeToolOutputRecord } from '../helpers/nativeMessages'

/** Decode the original native ACP result, without a complete-text supplement. */
export function readOpenCodeNativeOutput(snapshot: NativeMessageSnapshot, callId: string): NativeOutputReceipt {
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

/**
 * The preview markers of the shell output of OpenCode and of Kilo, which builds on OpenCode.
 *
 * Their shell tool reads the command output in a fiber of a scope that closes when the process exits, so output
 * still in the pipe can be lost, and the captured output can end at any line. A truncated preview then keeps only
 * the last lines that fit the line and byte limits (`ShellTool.run` in OpenCode 1.18.34 and Kilo 7.8.3). That tail
 * window can hold the middle line, and it can miss the last line. It never holds the first line: a preview that
 * declares an output path is truncated, and the truncation drops lines from the start of the captured output.
 * So the preview must hold some numbered line of the output, and it must not hold the first line.
 */
export function openCodeTailWindowMarkers(output: Pick<ComputedNativeToolOutput, 'firstMarker' | 'lineMarker'>): { previewMarkers: readonly string[], absentMarker: string } {
  return { previewMarkers: [output.lineMarker], absentMarker: output.firstMarker }
}
