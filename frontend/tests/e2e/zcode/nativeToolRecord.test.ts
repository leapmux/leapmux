import { describe, expect, it } from 'vitest'
import { MESSAGE_SUPPLEMENT_FIELD } from '../../../src/generated/contracts/worker-vocab'
import { ZCODE_EVENT, ZCODE_INVOCATION_FIELD, ZCODE_SUPPLEMENT, ZCODE_WORKER_METADATA } from '../../../src/generated/contracts/zcode-protocol'
import { MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeOutputSnapshot } from '../helpers/nativeOutputReaderCases'
import { readZcodeNativeToolResult } from './nativeToolRecord'

const SESSION = 'native-session'
const CALL = 'native-call'
const TOOL = 'GetWorkflowRun'
const MESSAGE = 'native-assistant-message'
const SPAN = 'opaque-worker-invocation'
const OWNER_ERROR = 'The native ZCode result has no exact invocation owner.'
function values() {
  const frame = {
    type: ZCODE_EVENT.ToolUpdated,
    sessionId: SESSION,
    payload: {
      kind: 'result',
      toolCallId: CALL,
      toolName: TOOL,
      assistantMessageId: MESSAGE,
      result: { success: true, content: 'computed42' },
    },
  }
  const invocation: Record<string, unknown> = {
    [ZCODE_INVOCATION_FIELD.SpanID]: SPAN,
    [ZCODE_INVOCATION_FIELD.CallID]: CALL,
    [ZCODE_INVOCATION_FIELD.MessageID]: MESSAGE,
    [ZCODE_INVOCATION_FIELD.SessionID]: SESSION,
    [ZCODE_INVOCATION_FIELD.ToolName]: TOOL,
  }
  const native = {
    sessionId: SESSION,
    messageId: MESSAGE,
    data: {
      type: 'tool',
      callID: CALL,
      tool: TOOL,
      state: {
        status: 'completed',
        input: { run_id: 'native-run' },
      },
    },
  }
  const provider: Record<string, unknown> = {
    type: frame.type,
    [ZCODE_SUPPLEMENT.Payload]: { kind: 'result', toolCallId: CALL },
    [ZCODE_SUPPLEMENT.NativeTool]: native,
  }
  return { frame, invocation, native, provider }
}
function snapshot(change?: (value: ReturnType<typeof values>) => void, includeNative = true) {
  const value = values()
  change?.(value)
  return nativeOutputSnapshot([
    {
      frame: value.frame,
      spanId: SPAN,
      spanType: TOOL,
      supplement: {
        [MESSAGE_SUPPLEMENT_FIELD.Metadata]: { [ZCODE_WORKER_METADATA.Invocation]: value.invocation },
        ...(includeNative
          ? { [MESSAGE_SUPPLEMENT_FIELD.Provider]: value.provider }
          : {}),
      },
    },
  ], { agentId: 'worker-agent', agentSessionId: SESSION })
}

describe('readZcodeNativeToolResult', () => {
  it('reads the opaque invocation without changing either stored byte array', () => {
    const input = snapshot()
    const original = input.messages[0]!
    const content = original.content.slice()
    const supplement = original.supplementalContent.slice()
    const record = readZcodeNativeToolResult(input, CALL, TOOL)
    expect(record.message).toBe(original)
    expect(record.message.spanId).toBe(SPAN)
    expect(record.message.spanId).not.toBe(CALL)
    expect(record.frame).toEqual(values().frame)
    expect(original.content).toEqual(content)
    expect(original.supplementalContent).toEqual(supplement)
  })
  it('accepts owned metadata before the native-tool supplement arrives', () => {
    expect(readZcodeNativeToolResult(snapshot(undefined, false), CALL, TOOL).frame).toEqual(values().frame)
  })
  it.each([
    undefined,
    null,
    [],
    false,
    'malformed',
    {},
  ])('rejects absent or malformed invocation metadata %j', (supplement) => {
    const input = nativeOutputSnapshot([
      {
        frame: values().frame,
        spanId: SPAN,
        spanType: TOOL,
        supplement,
      },
    ], { agentId: 'worker-agent', agentSessionId: SESSION })
    expect(() => readZcodeNativeToolResult(input, CALL, TOOL)).toThrow(OWNER_ERROR)
  })
  it('permits an unknown message identity when every supplied identity agrees', () => {
    const input = snapshot((value) => {
      value.invocation[ZCODE_INVOCATION_FIELD.MessageID] = ''
      value.frame.payload.assistantMessageId = ''
    })
    expect(readZcodeNativeToolResult(input, CALL, TOOL).message.spanId).toBe(SPAN)
  })
  it('does not count a scheduled opening as a second result', () => {
    const input = snapshot()
    const scheduled = nativeOutputSnapshot([
      {
        frame: {
          type: ZCODE_EVENT.ToolUpdated,
          sessionId: SESSION,
          payload: { kind: 'scheduled', toolCallId: CALL, toolName: TOOL },
        },
        spanId: SPAN,
        spanType: TOOL,
      },
    ], { agentId: 'worker-agent', agentSessionId: SESSION })
    input.messages.unshift(scheduled.messages[0]!)
    expect(readZcodeNativeToolResult(input, CALL, TOOL).frame).toEqual(values().frame)
  })
  it.each(Object.values(ZCODE_INVOCATION_FIELD))('rejects a contradictory invocation field %s', (field) => {
    expect(() => readZcodeNativeToolResult(snapshot((value) => {
      value.invocation[field] = 'foreign'
    }), CALL, TOOL)).toThrow(OWNER_ERROR)
  })
  it('requires the message identity field even when it records an unknown value', () => {
    expect(() => readZcodeNativeToolResult(snapshot((value) => {
      delete value.invocation[ZCODE_INVOCATION_FIELD.MessageID]
    }), CALL, TOOL)).toThrow(OWNER_ERROR)
  })
  it.each([
    'session',
    'message',
    'call',
    'tool',
    'provider type',
    'provider payload',
    'null native record',
  ])('rejects contradictory native provenance %s', (field) => {
    const input = snapshot((value) => {
      if (field === 'session')
        value.native.sessionId = 'foreign'
      if (field === 'message')
        value.native.messageId = 'foreign'
      if (field === 'call')
        value.native.data.callID = 'foreign'
      if (field === 'tool')
        value.native.data.tool = 'Read'
      if (field === 'provider type')
        value.provider.type = 'foreign'
      if (field === 'provider payload')
        value.provider[ZCODE_SUPPLEMENT.Payload] = { kind: 'result', toolCallId: 'foreign' }
      if (field === 'null native record')
        value.provider[ZCODE_SUPPLEMENT.NativeTool] = null
    })
    expect(() => readZcodeNativeToolResult(input, CALL, TOOL)).toThrow(OWNER_ERROR)
  })
  it('rejects an original native tool that contradicts its invocation', () => {
    expect(() => readZcodeNativeToolResult(snapshot((value) => {
      value.frame.payload.toolName = 'Read'
    }), CALL, TOOL)).toThrow(OWNER_ERROR)
  })
  it('rejects a different Worker source or tool type', () => {
    const source = snapshot()
    source.messages[0]!.source = MessageSource.USER
    expect(() => readZcodeNativeToolResult(source, CALL, TOOL)).toThrow(OWNER_ERROR)
    const tool = snapshot()
    tool.messages[0]!.spanType = 'Read'
    expect(() => readZcodeNativeToolResult(tool, CALL, TOOL)).toThrow(OWNER_ERROR)
  })
  it('rejects a foreign Worker session', () => {
    const input = snapshot()
    input.messages[0]!.agentSessionId = 'foreign'
    expect(() => readZcodeNativeToolResult(input, CALL, TOOL)).toThrow('exactly one accepted record')
  })
  it('rejects duplicate results across different opaque invocations', () => {
    const input = snapshot()
    const second = snapshot((value) => {
      value.invocation[ZCODE_INVOCATION_FIELD.SpanID] = 'another-opaque-invocation'
    })
    second.messages[0]!.spanId = 'another-opaque-invocation'
    input.messages.push(second.messages[0]!)
    expect(() => readZcodeNativeToolResult(input, CALL, TOOL)).toThrow('exactly one accepted record')
  })
})
