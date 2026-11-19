import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { OPENCODE_NATIVE_EVENT, OPENCODE_NATIVE_PART_TYPE, OPENCODE_NATIVE_TOOL_STATUS } from '../../../src/generated/contracts/opencode-native-protocol'
import { MessageCompletion } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickObject, pickString } from '../../../src/lib/jsonPick'
import { nativeMessageBody } from '../helpers/nativeMessages'

const TOOL_EVENTS = new Set<string>([
  OPENCODE_NATIVE_EVENT.ToolInputStarted,
  OPENCODE_NATIVE_EVENT.ToolCalled,
  OPENCODE_NATIVE_EVENT.ToolProgress,
  OPENCODE_NATIVE_EVENT.ToolSuccess,
  OPENCODE_NATIVE_EVENT.ToolFailed,
])

/** Read original tool bytes from the exact Worker agent and native session. */
export function openCodeNativeToolRecords(snapshot: NativeMessageSnapshot, callId: string) {
  if (!callId || !snapshot.agentId || !snapshot.agentSessionId)
    throw new Error('The native tool query requires an exact agent, session, and call ID.')
  return snapshot.messages.flatMap((message) => {
    if (message.agentSessionId !== snapshot.agentSessionId || !message.spanId)
      return []
    const frame = nativeMessageBody(message)
    if (!isObject(frame))
      return []
    if (frame.type === OPENCODE_NATIVE_PART_TYPE.Tool && frame.id === callId) {
      const state = pickObject(frame, 'state')
      if (!state)
        throw new Error('The recovered native tool has no state.')
      return [
        {
          message,
          frame,
          data: state,
          finished: message.completion !== MessageCompletion.UNSPECIFIED,
          status: pickString(state, 'status'),
        },
      ]
    }
    if (!TOOL_EVENTS.has(pickString(frame, 'type')))
      return []
    const data = pickObject(frame, 'data')
    if (!data || data.id !== callId || data.sessionID !== snapshot.agentSessionId)
      return []
    if (typeof data.assistantMessageID !== 'string' || data.assistantMessageID === '')
      throw new Error('The native tool frame has no assistant message identity.')
    const succeeded = frame.type === OPENCODE_NATIVE_EVENT.ToolSuccess
    const failed = frame.type === OPENCODE_NATIVE_EVENT.ToolFailed
    return [
      {
        message,
        frame,
        data,
        finished: succeeded || failed,
        status: succeeded
          ? OPENCODE_NATIVE_TOOL_STATUS.Completed
          : failed ? OPENCODE_NATIVE_TOOL_STATUS.Error : OPENCODE_NATIVE_TOOL_STATUS.Running,
      },
    ]
  })
}

/** Use the Worker span instead of rebuilding its encoded identity in a test. */
export function openCodeNativeToolRowId(snapshot: NativeMessageSnapshot, callId: string): string {
  const spans = [
    ...new Set(openCodeNativeToolRecords(snapshot, callId).map(record => record.message.spanId)),
  ]
  if (spans.length !== 1 || !spans[0])
    throw new Error(`The native call ${callId} matches ${spans.length} Worker spans. Exactly one is required.`)
  return spans[0]
}
