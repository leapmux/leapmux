import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { ComputedNativeToolOutput } from '../helpers/nativeToolOutput'
import type { NativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { isFilesystemPath } from '../../../src/lib/paths'
import { openCodeNativeToolRecords } from './nativeToolRecords'

/** Read the original completed native result without resolving any supplements. */
export function readOpenCodeNativeOutput(snapshot: NativeMessageSnapshot, callId: string): NativeOutputReceipt {
  const endings = openCodeNativeToolRecords(snapshot, callId).filter(record => record.finished)
  if (endings.length !== 1 || !endings[0])
    throw new Error('The native output query requires one completed tool result.')
  const { frame, message, data } = endings[0]
  const previewText = Array.isArray(data.content)
    ? data.content.flatMap(item => isObject(item) && item.type === 'text' && typeof item.text === 'string'
        ? [item.text]
        : []).join('\n')
    : ''
  const metadata = pickObject(data, 'metadata')
  const path = metadata?.outputPath ?? (metadata?.truncated === true
    ? /; full output saved to (.+?)\](?:\n\n\[output file .+ stopped early: .+\])?$/s.exec(previewText)?.[1]
    : undefined)
  if (!isFilesystemPath(path))
    throw new Error('The native OpenCode result has no exact filesystem output pointer.')
  return {
    paths: [path],
    previewText,
    frame,
    content: message.content,
  }
}

/** Native v2 shell previews retain the tail of a truncated output. */
export function openCodeTailWindowMarkers(output: Pick<ComputedNativeToolOutput, 'firstMarker' | 'lineMarker'>): { previewMarkers: readonly string[], absentMarker: string } {
  return {
    previewMarkers: [output.lineMarker],
    absentMarker: output.firstMarker,
  }
}
