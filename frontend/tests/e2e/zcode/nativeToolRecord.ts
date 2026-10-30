import type { NativeMessageSnapshot, NativeToolOutputRecord } from '../helpers/nativeMessages'
import { MESSAGE_SUPPLEMENT_FIELD } from '../../../src/generated/contracts/worker-vocab'
import { ZCODE_EVENT, ZCODE_INVOCATION_FIELD, ZCODE_SUPPLEMENT, ZCODE_WORKER_METADATA } from '../../../src/generated/contracts/zcode-protocol'
import { MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { readNativeToolOutputRecord } from '../helpers/nativeMessages'

const INVOCATION_OWNER = 'The native ZCode result has no exact invocation owner.'

function refuseInvocationOwner(): never {
  throw new Error(INVOCATION_OWNER)
}
function optionalIdentity(value: unknown): string | undefined {
  if (value === undefined)
    return undefined
  if (typeof value !== 'string')
    return refuseInvocationOwner()
  return value
}
function nonemptyIdentity(value: unknown): value is string {
  return typeof value === 'string' && value.trim() !== ''
}

/** Read one native result through its recorded opaque Worker invocation. */
export function readZcodeNativeToolResult(snapshot: NativeMessageSnapshot, callId: string, toolName: string): NativeToolOutputRecord {
  if (!toolName.trim())
    return refuseInvocationOwner()
  return readNativeToolOutputRecord(snapshot, {
    callId,
    accepts: (frame) => {
      const payload = pickObject(frame, 'payload')
      return frame.type === ZCODE_EVENT.ToolUpdated && frame.sessionId === snapshot.agentSessionId
        && payload?.kind === 'result' && payload.toolCallId === callId
    },
    spanId: (frame, message, supplement) => {
      const payload = pickObject(frame, 'payload')
      if (!isObject(supplement))
        return refuseInvocationOwner()
      const metadata = pickObject(supplement, MESSAGE_SUPPLEMENT_FIELD.Metadata)
      const invocation = pickObject(metadata, ZCODE_WORKER_METADATA.Invocation)
      const spanId = invocation?.[ZCODE_INVOCATION_FIELD.SpanID]
      const messageId = invocation?.[ZCODE_INVOCATION_FIELD.MessageID]
      if (!payload || !invocation || message.source !== MessageSource.AGENT
        || message.agentSessionId !== snapshot.agentSessionId || message.spanType !== toolName
        || !nonemptyIdentity(spanId) || spanId !== message.spanId
        || invocation[ZCODE_INVOCATION_FIELD.CallID] !== callId
        || invocation[ZCODE_INVOCATION_FIELD.SessionID] !== snapshot.agentSessionId
        || invocation[ZCODE_INVOCATION_FIELD.ToolName] !== toolName || typeof messageId !== 'string') {
        return refuseInvocationOwner()
      }
      const originalToolName = optionalIdentity(payload.toolName)
      if (originalToolName && originalToolName !== toolName)
        return refuseInvocationOwner()
      const messageIds = [
        messageId,
        optionalIdentity(payload.assistantMessageId),
      ]
        .filter((value): value is string => value !== undefined && value !== '')
      const provider = pickObject(supplement, MESSAGE_SUPPLEMENT_FIELD.Provider)
      if (provider && Object.hasOwn(provider, ZCODE_SUPPLEMENT.NativeTool)) {
        const native = pickObject(provider, ZCODE_SUPPLEMENT.NativeTool)
        const nativePayload = pickObject(provider, ZCODE_SUPPLEMENT.Payload)
        const data = pickObject(native, 'data')
        const nativeMessageId = native?.messageId
        if (!native || !nativePayload || !data || provider.type !== frame.type
          || nativePayload.kind !== payload.kind || nativePayload.toolCallId !== callId
          || native.sessionId !== snapshot.agentSessionId || !nonemptyIdentity(nativeMessageId)
          || data.type !== 'tool' || data.callID !== callId || data.tool !== toolName) {
          return refuseInvocationOwner()
        }
        const supplementalToolName = optionalIdentity(nativePayload.toolName)
        if (supplementalToolName && supplementalToolName !== toolName)
          return refuseInvocationOwner()
        const supplementalMessageId = optionalIdentity(nativePayload.assistantMessageId)
        if (supplementalMessageId)
          messageIds.push(supplementalMessageId)
        messageIds.push(nativeMessageId)
      }
      if (new Set(messageIds).size > 1)
        return refuseInvocationOwner()
      return spanId
    },
  })
}
