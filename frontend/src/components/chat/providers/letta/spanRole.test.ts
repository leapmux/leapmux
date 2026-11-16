import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { lettaNeedsRequest, lettaSpanRole } from './spanRole'

const start = {
  id: 'letta-msg-1',
  message_type: 'client_tool_start',
  tool_call_id: 'call-1',
  run_id: 'run-1',
  tool_name: 'Bash',
  tool_args: '{"command":"ls"}',
}

const nativeCall = {
  type: 'message',
  message_type: 'tool_call_message',
  run_id: 'run-1',
  tool_calls: [
    { tool_call_id: 'call-1', name: 'Read', arguments: '{"file_path":"note.txt"}' },
  ],
}

const answer = {
  type: 'message',
  id: 'synthetic-tool-return-final',
  message_type: 'tool_return_message',
  run_id: 'run-1',
  tool_call_id: 'call-1',
  status: 'success',
  tool_return: 'done',
}

const answeredEnd = {
  message_type: 'client_tool_end',
  run_id: 'run-1',
  tool_call_id: 'call-1',
  status: 'success',
  tool_return: 'done',
}

const progress = {
  ...answer,
  id: 'synthetic-tool-return-stream-call-1',
}

const emptyEnd = {
  message_type: 'client_tool_end',
  run_id: 'run-1',
  tool_call_id: 'call-1',
  status: 'error',
}

function row(parent: Record<string, unknown>, completion?: MessageCompletion) {
  return {
    ...input(parent, undefined, AgentProvider.LETTA),
    ...(completion !== undefined
      ? { completion }
      : {}),
  }
}

// The Worker stores the opening frame of a call again as its closing row when the
// turn or the process ends before Letta Code answers the call.
const RETAINED = [MessageCompletion.INTERRUPTED, MessageCompletion.ERROR, MessageCompletion.COMPLETE]
  .map(completion => [MessageCompletion[completion], completion] as const)

describe('lettaSpanRole', () => {
  it('reads a start frame and a native call frame as the request', () => {
    expect(lettaSpanRole(row(start))).toBe('request')
    expect(lettaSpanRole(row(nativeCall))).toBe('request')
  })

  it.each(RETAINED)('reads a retained start frame and native call frame as the result: %s', (_name, completion) => {
    expect(lettaSpanRole(row(start, completion))).toBe('result')
    expect(lettaSpanRole(row(nativeCall, completion))).toBe('result')
  })

  it('reads an answer as the result, and progress or an empty end as no side', () => {
    expect(lettaSpanRole(row(answer))).toBe('result')
    expect(lettaSpanRole(row(answeredEnd))).toBe('result')
    expect(lettaSpanRole(row(progress))).toBe('none')
    expect(lettaSpanRole(row(emptyEnd))).toBe('none')
  })

  it('reads a frame outside a tool call as other', () => {
    expect(lettaSpanRole(row({ message_type: 'assistant_message', content: 'hi' }))).toBe('other')
    expect(lettaSpanRole(row({ type: 'turn_finished', stop_reason: 'end_turn' }))).toBe('other')
  })
})

describe('lettaNeedsRequest', () => {
  it('answers true for an answer, which can omit the name and the arguments', () => {
    expect(lettaNeedsRequest(row(answer))).toBe(true)
    expect(lettaNeedsRequest(row(answeredEnd))).toBe(true)
  })

  it('answers false for a start frame and a native call frame, which state their own call', () => {
    expect(lettaNeedsRequest(row(start))).toBe(false)
    expect(lettaNeedsRequest(row(nativeCall))).toBe(false)
  })

  // A retained copy is the span's result, but it still holds the frame that states
  // the name and the arguments.
  it.each(RETAINED)('answers false for a retained start frame and native call frame: %s', (_name, completion) => {
    expect(lettaNeedsRequest(row(start, completion))).toBe(false)
    expect(lettaNeedsRequest(row(nativeCall, completion))).toBe(false)
  })

  it('answers false for progress and for an empty end, which draw nothing', () => {
    expect(lettaNeedsRequest(row(progress))).toBe(false)
    expect(lettaNeedsRequest(row(emptyEnd))).toBe(false)
  })
})
