import type { NativeControlFrame } from '../helpers/nativeControlWatch'
import { describe, expect, it } from 'vitest'
import { MCP_ELICITATION_METHOD } from '../../../src/generated/contracts/mcp-elicitation'
import { ControlResponseState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { diracQuestionControl } from './questionControl'

const QUESTION = 'Which color should I use?'

/** One control frame of a native `elicitation/create` request, as the Worker publishes the JSON-RPC line. */
function questionFrame(overrides: Partial<NativeControlFrame> & { message?: string } = {}): NativeControlFrame {
  const { message = QUESTION, ...frame } = overrides
  return {
    requestId: 'question-1',
    responseState: ControlResponseState.READY,
    payload: { jsonrpc: '2.0', id: 7, method: MCP_ELICITATION_METHOD.ACP, params: { sessionId: 'native-session', mode: 'form', message } },
    ...frame,
  }
}

describe('diracQuestionControl', () => {
  it('returns the one question request, and counts a replay of it once', () => {
    const frame = questionFrame()
    expect(diracQuestionControl([frame], QUESTION)).toBe(frame)
    expect(diracQuestionControl([frame, { ...frame }], QUESTION).requestId).toBe('question-1')
  })

  it('ignores a permission request and a later state of the question', () => {
    const permission = questionFrame({ requestId: 'permission-1', payload: { method: 'session/request_permission', params: { message: QUESTION } } })
    const completed = questionFrame({ responseState: ControlResponseState.COMPLETED, payload: {} })
    expect(diracQuestionControl([permission, questionFrame(), completed], QUESTION).requestId).toBe('question-1')
  })

  it('refuses a watch that saw no question request', () => {
    expect(() => diracQuestionControl([], QUESTION)).toThrow('exactly one elicitation request, not 0')
    expect(() => diracQuestionControl([questionFrame({ responseState: ControlResponseState.COMPLETED })], QUESTION)).toThrow('not 0')
  })

  it('refuses two distinct question requests', () => {
    expect(() => diracQuestionControl([questionFrame(), questionFrame({ requestId: 'question-2' })], QUESTION)).toThrow('not 2')
  })

  it('refuses a question request that does not hold the question', () => {
    expect(() => diracQuestionControl([questionFrame({ message: 'Which size should I use?' })], QUESTION)).toThrow('does not hold the question')
  })

  it('refuses an empty question', () => {
    expect(() => diracQuestionControl([questionFrame()], ' ')).toThrow('needs the text of its question')
  })
})
