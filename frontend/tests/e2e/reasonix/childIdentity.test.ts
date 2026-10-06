import { describe, expect, it } from 'vitest'
import { lastUserText, matchesRequest } from '../helpers/mockModelScript'
import { nativeFrameSnapshot, onlyNativeMessage } from '../helpers/nativeOutputReaderCases'
import { reasonixChildTaskId, reasonixChildTaskMatcher } from './childIdentity'

const encoder = new TextEncoder()
const marker = 'NATIVE_CHILD_MARKER'
const prompt = `${marker}: reply once.\n\nLEAPMUXE2ESCENARIO:current-task`
const actualPack = `<subagent-context event="SubagentStart">\nBefore acting, check the available skills and tools.\n</subagent-context>\n\n<workspace-context event="SubagentWorkspace">\nCurrent workspace: "/private/project"\n</workspace-context>\n\n## Task\n${prompt}\nDo not copy or reconstruct the parent session. Use only this pack plus tools.`
const nativeCall = { sessionUpdate: 'tool_call', toolCallId: 'spawn', title: 'read_only_task', rawInput: { description: 'Native display label', prompt } }

describe('reasonixChildTaskMatcher', () => {
  it('matches the captured native context prefix and exact Task marker', () => {
    expect(matchesRequest(reasonixChildTaskMatcher(marker), { protocol: 'openai-chat-completions', userText: actualPack, systemText: '', body: {} })).toBe(true)
  })

  it('does not match another child, a root prompt, or a historical task copy', () => {
    const matcher = reasonixChildTaskMatcher(marker)
    for (const userText of [actualPack.replace(marker, 'ANOTHER_CHILD'), prompt, actualPack.replace('## Task\n', '## Other\n')])
      expect(matchesRequest(matcher, { protocol: 'openai-chat-completions', userText, systemText: actualPack, body: {} })).toBe(false)
    const body = { messages: [{ role: 'user', content: actualPack }, { role: 'user', content: 'Run the next root task.' }], tools: [{ description: actualPack }] }
    expect(matchesRequest(matcher, { protocol: 'openai-chat-completions', userText: lastUserText(body), systemText: '', body })).toBe(false)
  })

  it('escapes marker punctuation and refuses an empty marker', () => {
    const special = 'NATIVE.[child]+(id)'
    expect(matchesRequest(reasonixChildTaskMatcher(special), { protocol: 'openai-chat-completions', userText: actualPack.replace(marker, special), systemText: '', body: {} })).toBe(true)
    expect(() => reasonixChildTaskMatcher('   ')).toThrow('nonempty task marker')
  })
})

describe('reasonixChildTaskId', () => {
  it('uses the exact native task call and ignores its display description', () => {
    expect(reasonixChildTaskId(nativeFrameSnapshot(nativeCall), 'spawn', prompt)).toBe('spawn')
  })

  it.each(['', ' '])('refuses an invalid native session before decoding a matching task frame: "%s"', (session) => {
    const source = nativeFrameSnapshot(nativeCall)
    source.agentSessionId = session
    const message = onlyNativeMessage(source)
    message.agentSessionId = session
    expect(() => reasonixChildTaskId(source, 'spawn', prompt)).toThrow('nonempty session ID')
    message.content = encoder.encode('{broken')
    expect(() => reasonixChildTaskId(source, 'spawn', prompt)).toThrow('nonempty session ID')
  })

  it('supports the native capability wrapper and repeated exact receipts', () => {
    const wrapped = { ...nativeCall, title: 'use_capability', rawInput: { action: 'call', capability_id: 'tool:read_only_task', arguments: nativeCall.rawInput } }
    expect(reasonixChildTaskId(nativeFrameSnapshot(wrapped, wrapped), 'spawn', prompt)).toBe('spawn')
  })

  it('refuses a wrong call, stale session, tool, or child prompt', () => {
    expect(() => reasonixChildTaskId(nativeFrameSnapshot({ ...nativeCall, toolCallId: 'other' }), 'spawn', prompt)).toThrow('no native parent receipt')
    expect(() => reasonixChildTaskId(nativeFrameSnapshot({ ...nativeCall, title: 'bash' }), 'spawn', prompt)).toThrow('another tool or child prompt')
    expect(() => reasonixChildTaskId(nativeFrameSnapshot({ ...nativeCall, rawInput: { prompt: 'WRONG_PROMPT' } }), 'spawn', prompt)).toThrow('another tool or child prompt')
    const stale = nativeFrameSnapshot(nativeCall)
    onlyNativeMessage(stale).agentSessionId = 'old-session'
    expect(() => reasonixChildTaskId(stale, 'spawn', prompt)).toThrow('no native parent receipt')
  })

  it('refuses missing receipts, malformed input, and invalid native JSON', () => {
    expect(() => reasonixChildTaskId(nativeFrameSnapshot(), 'spawn', prompt)).toThrow('no native parent receipt')
    expect(() => reasonixChildTaskId(nativeFrameSnapshot({ ...nativeCall, rawInput: null }), 'spawn', prompt)).toThrow('another tool or child prompt')
    const source = nativeFrameSnapshot(nativeCall)
    onlyNativeMessage(source).content = encoder.encode('{broken')
    expect(() => reasonixChildTaskId(source, 'spawn', prompt)).toThrow('invalid JSON')
  })
})
