import { describe, expect, it } from 'vitest'
import { matchesRequest } from '../helpers/mockModelScript'
import { nativeFrameSnapshot, onlyNativeMessage } from '../helpers/nativeOutputReaderCases'
import { gooseChildTaskId, gooseChildTaskMatcher } from './childIdentity'

describe('gooseChildTaskMatcher', () => {
  const task = 'NATIVECHILDTASK1 report one word.'
  const selects = (userText: string, matched = task) => matchesRequest(gooseChildTaskMatcher(matched), { protocol: 'openai-chat-completions', userText, systemText: '', body: {} })

  it('selects the child turn with and without the Subagent ID line of Goose', () => {
    expect(selects(`${task}\n\nSCENARIO_MARK`)).toBe(true)
    expect(selects(`Subagent ID: 20260101_7\n\n${task}`)).toBe(true)
  })

  it('refuses a parent turn that quotes the task after text of its own', () => {
    expect(selects(`Delegate this: ${task}`)).toBe(false)
  })

  it('reads regular expression syntax in the task as plain text', () => {
    expect(selects('Read a.b once', 'Read a.b once')).toBe(true)
    expect(selects('Read aXb once', 'Read a.b once')).toBe(false)
  })

  it('refuses an empty task', () => {
    expect(() => gooseChildTaskMatcher(' ')).toThrow('nonempty task')
  })
})

const encoder = new TextEncoder()
const prompt = 'ACTUAL_CHILD_TASK\n\nLEAPMUXE2ESCENARIO:current-task'
function event(id = 'spawn', instructions = prompt) {
  return { sessionUpdate: 'tool_call', toolCallId: id, title: 'delegate', rawInput: { instructions }, _meta: { goose: { toolCall: { toolName: 'delegate', extensionName: 'summon' } } } }
}

describe('gooseChildTaskId', () => {
  it('uses the actual delegate call and prompt independently of its display title', () => {
    expect(gooseChildTaskId(nativeFrameSnapshot(event('other'), { ...event(), title: 'Another native label' }), 'spawn', prompt)).toBe('spawn')
  })

  it.each(['', ' '])('refuses an invalid native session before decoding a matching delegate frame: "%s"', (session) => {
    const source = nativeFrameSnapshot(event())
    source.agentSessionId = session
    const message = onlyNativeMessage(source)
    message.agentSessionId = session
    expect(() => gooseChildTaskId(source, 'spawn', prompt)).toThrow('nonempty session ID')
    message.content = encoder.encode('{broken')
    expect(() => gooseChildTaskId(source, 'spawn', prompt)).toThrow('nonempty session ID')
  })

  it('retains repeated native receipts with the same identity and prompt', () => {
    expect(gooseChildTaskId(nativeFrameSnapshot(event(), event()), 'spawn', prompt)).toBe('spawn')
  })

  it('refuses absent calls, stale sessions, and another exact task prompt', () => {
    expect(() => gooseChildTaskId(nativeFrameSnapshot(), 'spawn', prompt)).toThrow('no native parent receipt')
    expect(() => gooseChildTaskId(nativeFrameSnapshot(event('other')), 'spawn', prompt)).toThrow('no native parent receipt')
    expect(() => gooseChildTaskId(nativeFrameSnapshot(event('spawn', 'WRONG_CHILD_PROMPT')), 'spawn', prompt)).toThrow('another child prompt')
    const stale = nativeFrameSnapshot(event())
    onlyNativeMessage(stale).agentSessionId = 'old-session'
    expect(() => gooseChildTaskId(stale, 'spawn', prompt)).toThrow('no native parent receipt')
  })

  it.each([null, {}, { goose: [] }, { goose: { toolCall: null } }, { goose: { toolCall: { toolName: 'shell', extensionName: 'developer' } } }])('refuses missing or unrelated native delegate metadata: %j', (_meta) => {
    expect(() => gooseChildTaskId(nativeFrameSnapshot({ ...event(), _meta }), 'spawn', prompt)).toThrow('not a native summon delegate')
  })

  it('refuses conflicting receipts and invalid native bytes', () => {
    expect(() => gooseChildTaskId(nativeFrameSnapshot(event(), event('spawn', 'OTHER_PROMPT')), 'spawn', prompt)).toThrow('another child prompt')
    const source = nativeFrameSnapshot(event())
    onlyNativeMessage(source).content = encoder.encode('{broken')
    expect(() => gooseChildTaskId(source, 'spawn', prompt)).toThrow('invalid JSON')
  })

  it('requires nonempty exact call and task identities', () => {
    expect(() => gooseChildTaskId(nativeFrameSnapshot(event()), '', prompt)).toThrow('call ID and prompt')
    expect(() => gooseChildTaskId(nativeFrameSnapshot(event()), 'spawn', '   ')).toThrow('call ID and prompt')
  })
})
