import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { NativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { pickObject } from '../../../src/lib/jsonPick'
import { isFilesystemPath } from '../../../src/lib/paths'
import { readNativeToolOutputRecord } from '../helpers/nativeMessages'

/** Read one original native completed item and its declared filesystem path. */
export function readCodewhaleNativeOutput(snapshot: NativeMessageSnapshot, callId: string): NativeOutputReceipt {
  const record = readNativeToolOutputRecord(snapshot, {
    callId,
    spanId: callId,
    accepts: frame => frame.event === 'item.completed' && frame.thread_id === snapshot.agentSessionId
      && pickObject(pickObject(pickObject(frame, 'payload'), 'item'), 'metadata')?.tool_use_id === callId,
  })
  const item = pickObject(pickObject(record.frame, 'payload'), 'item')
  const metadata = pickObject(item, 'metadata')
  const previewText = typeof item?.detail === 'string' ? item.detail : ''
  const footer = /\[Showing lines \d+-\d+ of \d+ \([^\r\n]+ limit\)\. Full output: ([^\]\r\n]+)\]$/u.exec(previewText)?.[1]
  const path = metadata?.spillover_path ?? footer
  if (!isFilesystemPath(path))
    throw new Error('The native Codewhale result has no filesystem pointer.')
  if (metadata?.spillover_path !== undefined) {
    const parts = path.split(/[\\/]/u)
    if (!metadata.artifact_session_id || metadata.artifact_relative_path !== `artifacts/${metadata.artifact_id}.txt`
      || parts.at(-1) !== `${metadata.artifact_id}.txt` || parts.at(-3) !== metadata.artifact_session_id) {
      throw new Error('The native Codewhale path differs from its exact native owner.')
    }
  }
  return { paths: [path], previewText, frame: record.frame, content: record.message.content }
}
