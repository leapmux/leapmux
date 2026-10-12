import { describe, expect, it } from 'vitest'
import { commandCodeToolCompleted } from './toolCompleted'

function frame(event: Record<string, unknown>, type = 'event'): Record<string, unknown> {
  return { type, event }
}

describe('commandCodeToolCompleted', () => {
  const completed = commandCodeToolCompleted('read_file', 'call-1')

  it('selects the completion of the exact call and tool', () => {
    expect(completed(frame({ type: 'tool_completed', toolName: 'read_file', toolCallId: 'call-1', result: [] }))).toBe(true)
  })

  it.each([
    ['another call', frame({ type: 'tool_completed', toolName: 'read_file', toolCallId: 'call-2' })],
    ['another tool', frame({ type: 'tool_completed', toolName: 'shell_command', toolCallId: 'call-1' })],
    ['another event', frame({ type: 'tool_errored', toolName: 'read_file', toolCallId: 'call-1' })],
    ['another frame kind', frame({ type: 'tool_completed', toolName: 'read_file', toolCallId: 'call-1' }, 'response')],
    ['a frame with no event', { type: 'event' }],
  ])('refuses %s', (_label, value) => {
    expect(completed(value)).toBe(false)
  })

  it.each([['', 'call-1'], ['read_file', '']])('refuses an empty tool name or call ID: %j %j', (toolName, callId) => {
    expect(() => commandCodeToolCompleted(toolName, callId)).toThrow('requires a tool name and a call ID')
  })
})
