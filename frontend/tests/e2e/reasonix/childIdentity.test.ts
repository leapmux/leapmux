import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { lastUserText, matchesRequest } from '../helpers/mockModelScript'
import { reasonixChildTaskId, reasonixChildTaskMatcher } from './childIdentity'

const encoder = new TextEncoder()
const marker = 'NATIVE_CHILD_MARKER'
const prompt = `${marker}: reply once.\n\nLEAPMUXE2ESCENARIO:current-task`
const actualPack = `<subagent-context event="SubagentStart">\nBefore acting, check the available skills and tools.\n</subagent-context>\n\n<workspace-context event="SubagentWorkspace">\nCurrent workspace: "/private/project"\n</workspace-context>\n\n## Task\n${prompt}\nDo not copy or reconstruct the parent session. Use only this pack plus tools.`
function snapshot(...bodies: unknown[]): NativeMessageSnapshot {
  return { agentId: 'parent', agentSessionId: 'native-session', messages: bodies.map((body, index) => create(AgentChatMessageSchema, {
    id: `frame-${index}`,
    agentSessionId: 'native-session',
    contentCompression: ContentCompression.NONE,
    content: encoder.encode(JSON.stringify(body)),
  })) }
}
function onlyMessage(source: NativeMessageSnapshot) {
  expect(source.messages).toHaveLength(1)
  const message = source.messages[0]
  if (!message)
    throw new Error('The native Reasonix test fixture contains no message.')
  return message
}
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
    expect(reasonixChildTaskId(snapshot(nativeCall), 'spawn', prompt)).toBe('spawn')
  })

  it.each(['', ' '])('refuses an invalid native session before decoding a matching task frame: "%s"', (session) => {
    const source = snapshot(nativeCall)
    source.agentSessionId = session
    const message = onlyMessage(source)
    message.agentSessionId = session
    expect(() => reasonixChildTaskId(source, 'spawn', prompt)).toThrow('nonempty session ID')
    message.content = encoder.encode('{broken')
    expect(() => reasonixChildTaskId(source, 'spawn', prompt)).toThrow('nonempty session ID')
  })

  it('supports the native capability wrapper and repeated exact receipts', () => {
    const wrapped = { ...nativeCall, title: 'use_capability', rawInput: { action: 'call', capability_id: 'tool:read_only_task', arguments: nativeCall.rawInput } }
    expect(reasonixChildTaskId(snapshot(wrapped, wrapped), 'spawn', prompt)).toBe('spawn')
  })

  it('refuses a wrong call, stale session, tool, or child prompt', () => {
    expect(() => reasonixChildTaskId(snapshot({ ...nativeCall, toolCallId: 'other' }), 'spawn', prompt)).toThrow('no native parent receipt')
    expect(() => reasonixChildTaskId(snapshot({ ...nativeCall, title: 'bash' }), 'spawn', prompt)).toThrow('another tool or child prompt')
    expect(() => reasonixChildTaskId(snapshot({ ...nativeCall, rawInput: { prompt: 'WRONG_PROMPT' } }), 'spawn', prompt)).toThrow('another tool or child prompt')
    const stale = snapshot(nativeCall)
    onlyMessage(stale).agentSessionId = 'old-session'
    expect(() => reasonixChildTaskId(stale, 'spawn', prompt)).toThrow('no native parent receipt')
  })

  it('refuses missing receipts, malformed input, and invalid native JSON', () => {
    expect(() => reasonixChildTaskId(snapshot(), 'spawn', prompt)).toThrow('no native parent receipt')
    expect(() => reasonixChildTaskId(snapshot({ ...nativeCall, rawInput: null }), 'spawn', prompt)).toThrow('another tool or child prompt')
    const source = snapshot(nativeCall)
    onlyMessage(source).content = encoder.encode('{broken')
    expect(() => reasonixChildTaskId(source, 'spawn', prompt)).toThrow('invalid JSON')
  })
})
