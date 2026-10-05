import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageCompletion, MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexAppliedFileChange, requireCodexPatchResult } from './appliedFileChange'

const sessionId = 'native-thread'
const item = { id: 'actual-file-item', type: 'fileChange', status: 'completed', changes: [{ path: '/private/file.txt', kind: 'add', diff: 'ACTUAL_FILE42\n' }] }
const payload = (nativeItem: unknown) => ({ threadId: sessionId, turnId: 'native-turn', item: nativeItem })
const encoder = new TextEncoder()
function storedMessage(id: string, seq: bigint, body: unknown): AgentChatMessage {
  return create(AgentChatMessageSchema, {
    id,
    seq,
    source: MessageSource.AGENT,
    agentProvider: AgentProvider.CODEX,
    spanId: item.id,
    spanType: 'fileChange',
    agentSessionId: sessionId,
    // The Worker stores a native Codex item with no Worker completion.
    completion: MessageCompletion.UNSPECIFIED,
    contentCompression: ContentCompression.NONE,
    content: encoder.encode(JSON.stringify(body)),
  })
}
const started = storedMessage('native-started', 41n, payload({ ...item, status: 'inProgress', changes: [] }))
const completed = storedMessage('native-completed', 42n, payload(item))

describe('codexAppliedFileChange', () => {
  it('reads the actual params-only file items that Worker persists for the native span', () => {
    // The Worker stores event.params. It does not store the JSON-RPC method or envelope.
    expect(codexAppliedFileChange([started, completed], sessionId, item.id, '/private/file.txt')).toEqual(item.changes[0])
  })
  it('requires the matching actual native started and completed item with the exact file diff', () => {
    expect(codexAppliedFileChange([started, completed], sessionId, item.id, '/private/file.txt')).toEqual(item.changes[0])
  })
  it('reads the current native tagged add operation through the provider parser', () => {
    const tagged = storedMessage('native-completed', 42n, payload({ ...item, changes: [{ ...item.changes[0], kind: { type: 'add' } }] }))
    expect(codexAppliedFileChange([started, tagged], sessionId, item.id, '/private/file.txt')).toMatchObject({ path: '/private/file.txt', diff: 'ACTUAL_FILE42\n' })
  })
  it.each([
    { label: 'empty span', messages: [] },
    { label: 'start only', messages: [started] },
    { label: 'completion only', messages: [completed] },
    { label: 'duplicate completion', messages: [started, completed, completed] },
    { label: 'wrong item ID', messages: [started, storedMessage('native-completed', 42n, payload({ ...item, id: 'another-item' }))] },
    { label: 'failed item', messages: [started, storedMessage('native-completed', 42n, payload({ ...item, status: 'failed' }))] },
    { label: 'wrong item type', messages: [started, storedMessage('native-completed', 42n, payload({ ...item, type: 'commandExecution' }))] },
    { label: 'wrong diff path', messages: [started, storedMessage('native-completed', 42n, payload({ ...item, changes: [{ path: '/another/file.txt', kind: 'add', diff: 'ACTUAL_FILE42\n' }] }))] },
  ])('refuses an absent, duplicated, failed, or unrelated applied native item: $label', ({ messages }) => {
    expect(() => codexAppliedFileChange(messages, sessionId, item.id, '/private/file.txt')).toThrow()
  })
  it.each([MessageCompletion.COMPLETE, MessageCompletion.INTERRUPTED, MessageCompletion.ERROR])('refuses a completed row that carries the Worker completion %s', (completion) => {
    const finishedByWorker = create(AgentChatMessageSchema, { ...completed, completion })
    expect(() => codexAppliedFileChange([started, finishedByWorker], sessionId, item.id, '/private/file.txt')).toThrow('did not report a completed applied change')
  })
  it('refuses native completion before the same item starts', () => {
    const earlyCompleted = create(AgentChatMessageSchema, { ...completed, seq: 40n })
    expect(() => codexAppliedFileChange([earlyCompleted, started], sessionId, item.id, '/private/file.txt')).toThrow('before its actual start')
  })
  it.each([
    { label: 'empty message ID', fields: { id: '' } },
    { label: 'negative sequence', fields: { seq: -1n } },
    { label: 'equal sequence', fields: { seq: 41n } },
    { label: 'duplicate message ID', fields: { id: started.id } },
    { label: 'user message source', fields: { source: MessageSource.USER } },
    { label: 'wrong provider', fields: { agentProvider: AgentProvider.DROID } },
    { label: 'wrong span ID', fields: { spanId: 'another-span' } },
    { label: 'wrong span type', fields: { spanType: 'commandExecution' } },
    { label: 'error completion', fields: { completion: MessageCompletion.ERROR } },
    { label: 'interrupted completion', fields: { completion: MessageCompletion.INTERRUPTED } },
  ])('refuses invalid Worker metadata on the exact native file result: $label', ({ fields }) => {
    expect(() => codexAppliedFileChange([started, create(AgentChatMessageSchema, { ...completed, ...fields })], sessionId, item.id, '/private/file.txt')).toThrow()
  })
  it.each([
    { body: { ...payload(item), threadId: 'another-thread' } },
    { body: { ...payload(item), turnId: '' } },
    { body: { ...payload(item), turnId: 'another-turn' } },
    { body: { method: 'item/completed', params: payload(item) } },
  ])('refuses unrelated thread, turn, or envelope data inside the stored params: %j', ({ body }) => {
    expect(() => codexAppliedFileChange([started, storedMessage('native-completed', 42n, body)], sessionId, item.id, '/private/file.txt')).toThrow()
  })
  it('excludes old-session spans while preserving the exact current session and full sequence values', () => {
    const old = [started, completed].map(message => create(AgentChatMessageSchema, { ...message, agentSessionId: 'old-session' }))
    const currentStart = create(AgentChatMessageSchema, { ...started, seq: 9223372036854775806n })
    const currentEnd = create(AgentChatMessageSchema, { ...completed, seq: 9223372036854775807n })
    expect(codexAppliedFileChange([...old, currentStart, currentEnd], sessionId, item.id, '/private/file.txt')).toEqual(item.changes[0])
  })
  it('preserves sequence zero and an explicitly empty native diff', () => {
    const currentStart = create(AgentChatMessageSchema, { ...started, seq: 0n })
    const currentEnd = storedMessage('native-completed', 1n, payload({ ...item, changes: [{ path: '/private/file.txt', kind: 'add', diff: '' }] }))
    expect(codexAppliedFileChange([currentStart, currentEnd], sessionId, item.id, '/private/file.txt')).toEqual({ path: '/private/file.txt', kind: 'add', diff: '' })
  })
})

describe('requireCodexPatchResult', () => {
  function request(output: unknown, callId = 'actual-patch'): MockModelRequestRecord {
    return { protocol: 'openai-responses', path: '/v1/responses', body: { input: [{ type: 'custom_tool_call_output', call_id: callId, output }] } }
  }
  const blocks = [{ type: 'input_text', text: 'Script completed\nWall time 0.0 seconds\nOutput:\n' }, { type: 'input_text', text: '{}' }]
  it('accepts the actual empty return from the exact successful native code cell', () => {
    expect(() => requireCodexPatchResult(request(blocks), 'actual-patch')).not.toThrow()
  })
  it.each([
    { output: [] },
    { output: [blocks[0]] },
    { output: [blocks[1]] },
    { output: [...blocks, blocks[1]] },
    { output: [{ type: 'input_text', text: 'Script failed\n' }, blocks[1]] },
  ])('refuses missing or unsuccessful native patch completion: %j', ({ output }) => {
    expect(() => requireCodexPatchResult(request(output), 'actual-patch')).toThrow()
  })
  it('refuses another call result even when its completion looks successful', () => {
    expect(() => requireCodexPatchResult(request(blocks, 'other-patch'), 'actual-patch')).toThrow()
  })
})
