import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { classifyLettaMessage } from './classification'
import { lettaSpanRole } from './spanRole'

describe('classifyLettaMessage', () => {
  it('recognizes a native child tool_call_message as a request', () => {
    const frame = {
      type: 'message',
      message_type: 'tool_call_message',
      tool_calls: [{ tool_call_id: 'call-read-native', name: 'Read', arguments: '{"file_path":"note.txt"}' }],
    }
    const resolved = input(frame, undefined, AgentProvider.LETTA)

    expect(classifyLettaMessage(resolved)).toEqual({ kind: 'tool_use' })
    expect(lettaSpanRole(resolved)).toBe('request')
  })
})
