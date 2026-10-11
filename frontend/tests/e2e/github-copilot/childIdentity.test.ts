import { describe, expect, it } from 'vitest'
import { matchesRequest } from '../helpers/mockModelScript'
import { nativeFrameSnapshot, onlyNativeMessage } from '../helpers/nativeOutputReaderCases'
import { copilotChildTaskId, copilotChildTaskMatcher } from './childIdentity'

describe('copilotChildTaskMatcher', () => {
  const task = 'NATIVECHILDTASK1 report one word.'
  const selects = (userText: string, matched = task) => matchesRequest(copilotChildTaskMatcher(matched), { protocol: 'openai-chat-completions', userText, systemText: '', body: {} })

  it('selects the child turn after the datetime block that Copilot puts first', () => {
    expect(selects(`<current_datetime>2026-10-06T00:00:00Z</current_datetime>\n${task}\n\nSCENARIO_MARK`)).toBe(true)
    expect(selects(task)).toBe(true)
  })

  it('refuses a turn that holds the task inside a line', () => {
    expect(selects(`Delegate this: ${task}`)).toBe(false)
  })

  it('reads regular expression syntax in the task as plain text', () => {
    expect(selects('Read a.b once', 'Read a.b once')).toBe(true)
    expect(selects('Read aXb once', 'Read a.b once')).toBe(false)
  })

  it('refuses an empty task', () => {
    expect(() => copilotChildTaskMatcher('')).toThrow('nonempty task')
  })
})

const encoder = new TextEncoder()
function event(agentId: unknown = 'actual_native_child', toolCallId = 'spawn', sessionId = 'native-session') {
  return { method: 'session.event', params: { sessionId, event: { type: 'subagent.started', agentId, data: { toolCallId } } } }
}

describe('copilotChildTaskId', () => {
  it('selects the actual native child through the exact spawning call and session', () => {
    expect(copilotChildTaskId(nativeFrameSnapshot(event('other', 'other-call'), event()), 'spawn')).toBe('actual_native_child')
  })

  it.each(['', ' '])('refuses an invalid native session before decoding a matching child frame: "%s"', (session) => {
    const source = nativeFrameSnapshot(event('actual_native_child', 'spawn', session))
    source.agentSessionId = session
    const message = onlyNativeMessage(source)
    message.agentSessionId = session
    expect(() => copilotChildTaskId(source, 'spawn')).toThrow('nonempty session ID')
    message.content = encoder.encode('{broken')
    expect(() => copilotChildTaskId(source, 'spawn')).toThrow('nonempty session ID')
  })

  it('accepts repeated starts for the same child but refuses conflicting identities', () => {
    expect(copilotChildTaskId(nativeFrameSnapshot(event(), event()), 'spawn')).toBe('actual_native_child')
    expect(() => copilotChildTaskId(nativeFrameSnapshot(event(), event('conflicting-child')), 'spawn')).toThrow('one native child')
  })

  it.each([undefined, null, '', '   ', 0, [], {}])('refuses a malformed actual native child ID: %j', (id) => {
    const original = event()
    const body = { ...original, params: { ...original.params, event: { ...original.params.event, agentId: id } } }
    expect(() => copilotChildTaskId(nativeFrameSnapshot(body), 'spawn')).toThrow('no agent ID')
  })

  it('refuses absent starts, wrong calls, and wrong native sessions', () => {
    expect(() => copilotChildTaskId(nativeFrameSnapshot(), 'spawn')).toThrow('one native child')
    expect(() => copilotChildTaskId(nativeFrameSnapshot(event('other', 'other-call')), 'spawn')).toThrow('one native child')
    expect(() => copilotChildTaskId(nativeFrameSnapshot(event('other', 'spawn', 'other-session')), 'spawn')).toThrow('one native child')
    const stale = nativeFrameSnapshot(event())
    onlyNativeMessage(stale).agentSessionId = 'old-session'
    expect(() => copilotChildTaskId(stale, 'spawn')).toThrow('one native child')
  })

  it('ignores valid unrelated events but refuses malformed relevant events', () => {
    expect(copilotChildTaskId(nativeFrameSnapshot({ type: 'user' }, { method: 'session.event', params: { sessionId: 'native-session', event: { type: 'assistant.message' } } }, event()), 'spawn')).toBe('actual_native_child')
    expect(() => copilotChildTaskId(nativeFrameSnapshot({ method: 'session.event', params: null }), 'spawn')).toThrow('invalid parameters')
    expect(() => copilotChildTaskId(nativeFrameSnapshot({ method: 'session.event', params: { sessionId: 'native-session', event: null } }), 'spawn')).toThrow('event object')
    expect(() => copilotChildTaskId(nativeFrameSnapshot({ method: 'session.event', params: { sessionId: 'native-session', event: { type: 'subagent.started', data: null } } }), 'spawn')).toThrow('data object')
  })

  it('preserves native decode failures and refuses an empty call ID', () => {
    const source = nativeFrameSnapshot(event())
    onlyNativeMessage(source).content = encoder.encode('{broken')
    expect(() => copilotChildTaskId(source, 'spawn')).toThrow('invalid JSON')
    expect(() => copilotChildTaskId(nativeFrameSnapshot(event()), '')).toThrow('spawn call ID')
  })
})
