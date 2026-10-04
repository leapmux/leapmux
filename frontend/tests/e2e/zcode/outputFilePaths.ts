import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { MESSAGE_SUPPLEMENT_FIELD } from '../../../src/generated/contracts/worker-vocab'
import { ZCODE_EVENT, ZCODE_SUPPLEMENT } from '../../../src/generated/contracts/zcode-protocol'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { isFilesystemPath } from '../../../src/lib/paths'
import { readNativeToolOutputRecord } from '../helpers/nativeMessages'

function nativeFileSegment(value: string): string {
  return value.replace(/[^\w.-]/g, '_').slice(0, 120) || 'unknown'
}

/** Read the original inline result and its exact native tool serialization path. */
export function readZcodeNativeOutput(snapshot: NativeMessageSnapshot, callId: string, toolName: string) {
  const record = readNativeToolOutputRecord(snapshot, {
    callId,
    spanId: callId,
    accepts: frame => frame.type === ZCODE_EVENT.ToolUpdated && frame.sessionId === snapshot.agentSessionId
      && pickObject(frame, 'payload')?.kind === 'result' && pickObject(frame, 'payload')?.toolCallId === callId,
  })
  const provider = isObject(record.supplement) ? pickObject(record.supplement, MESSAGE_SUPPLEMENT_FIELD.Provider) : undefined
  const payload = pickObject(provider, ZCODE_SUPPLEMENT.Payload)
  const native = pickObject(provider, ZCODE_SUPPLEMENT.NativeTool)
  const data = pickObject(native, 'data')
  const state = pickObject(data, 'state')
  const serialization = pickObject(pickObject(state, 'metadata'), 'serialization')
  const path = serialization?.artifactPath
  const result = pickObject(pickObject(record.frame, 'payload'), 'result')
  if (provider?.type !== record.frame.type || payload?.kind !== 'result' || payload.toolCallId !== callId
    || native?.sessionId !== snapshot.agentSessionId || data?.type !== 'tool' || data.callID !== callId || data.tool !== toolName
    || record.message.spanType !== toolName || state?.status !== 'completed' || serialization?.budgetStrategy !== 'artifact'
    || !isFilesystemPath(path) || typeof result?.content !== 'string') {
    throw new Error('The native ZCode output pointer has no exact call and session owner.')
  }
  const parts = path.split(/[\\/]/u)
  const leaf = parts.at(-1) ?? ''
  const prefix = `${nativeFileSegment(callId)}-`
  if (parts.at(-2) !== nativeFileSegment(snapshot.agentSessionId) || !leaf.startsWith(prefix)
    || !/^tool-result-[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}\.txt$/iu.test(leaf.slice(prefix.length))) {
    throw new Error('The native ZCode path belongs to another call or session.')
  }
  return { paths: [path], previewText: result.content, frame: record.frame, supplement: record.supplement, native, content: record.message.content }
}
