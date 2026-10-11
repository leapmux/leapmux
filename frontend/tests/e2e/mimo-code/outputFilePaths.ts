import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { NativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { pickObject, pickString } from '../../../src/lib/jsonPick'
import { isFilesystemPath } from '../../../src/lib/paths'
import { readNativeToolOutputRecord } from '../helpers/nativeMessages'

/** Decode the original native HTTP result and its declared output path. */
export function readMiMoNativeOutput(snapshot: NativeMessageSnapshot, callId: string): NativeOutputReceipt {
  const record = readNativeToolOutputRecord(snapshot, {
    callId,
    spanId: frame => pickString(pickObject(pickObject(frame, 'properties'), 'part'), 'id'),
    accepts: (frame) => {
      const part = pickObject(pickObject(frame, 'properties'), 'part')
      return frame.type === 'message.part.updated' && part?.type === 'tool' && part.tool === 'bash' && typeof part.id === 'string' && part.id !== ''
        && part.callID === callId && part.sessionID === snapshot.agentSessionId
        && pickObject(part, 'state')?.status === 'completed'
    },
  })
  const state = pickObject(pickObject(pickObject(record.frame, 'properties'), 'part'), 'state')
  const metadata = pickObject(state, 'metadata')
  const path = metadata?.outputPath
  if (!isFilesystemPath(path) || !/[\\/]tool-output[\\/]tool_[A-Za-z0-9]+$/u.test(path))
    throw new Error('The native MiMo result has no filesystem output pointer.')
  const previewText = typeof metadata?.output === 'string' ? metadata.output : typeof state?.output === 'string' ? state.output : ''
  return { paths: [path], previewText, frame: record.frame, content: record.message.content }
}
