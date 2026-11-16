import { describe, expect, it } from 'vitest'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { classifyOpenCodeMessage } from './classification'

describe('classifyOpenCodeMessage', () => {
  it('classifies live and recovered native tool sides', () => {
    const live = {
      type: 'session.tool.called',
      data: {
        id: 'c',
        name: 'read',
        sessionID: 'ses_1',
        assistantMessageID: 'msg_1',
        input: { path: '/file' },
      },
    }
    expect(classifyOpenCodeMessage(input(live))).toEqual({ kind: 'tool_use' })
    const recovered = {
      type: 'tool',
      id: 'c',
      name: 'read',
      state: {
        status: 'completed',
        input: {},
        content: [
          { type: 'text', text: 'saved' },
        ],
      },
    }
    expect(classifyOpenCodeMessage(input(recovered))).toEqual({ kind: 'tool_use' })
    expect(classifyOpenCodeMessage({
      ...input(recovered),
      completion: MessageCompletion.COMPLETE,
    })).toEqual({ kind: 'tool_result' })
  })

  it('keeps native and Worker-owned user records readable', () => {
    expect(classifyOpenCodeMessage(input({ type: 'user', text: 'native' }))).toEqual({ kind: 'user_content' })
    expect(classifyOpenCodeMessage(input({ content: 'worker' }))).toEqual({ kind: 'user_content' })
    expect(classifyOpenCodeMessage(input({ content: 'plan', planExecution: true }))).toEqual({ kind: 'plan_execution' })
  })

  it('recognizes execution endings and hides known bookkeeping only', () => {
    for (const type of [
      'session.execution.succeeded',
      'session.execution.failed',
      'session.execution.interrupted',
      'idle',
    ])
      expect(classifyOpenCodeMessage(input({ type }))).toEqual({ kind: 'result_divider' })
    expect(classifyOpenCodeMessage(input({ type: 'session.step.started' }))).toEqual({ kind: 'hidden' })
    expect(classifyOpenCodeMessage(input({ type: 'session.future-record' }))).toEqual({ kind: 'unknown' })
  })
})
