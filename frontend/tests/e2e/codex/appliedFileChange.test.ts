import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { AgentProvider, MessageCompletion, MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexAppliedFileChange, requireCodexPatchResult } from './appliedFileChange'

const sessionId = 'native-thread'
const item = { id: 'actual-file-item', type: 'fileChange', status: 'completed', changes: [{ path: '/private/file.txt', kind: 'add', diff: 'ACTUAL_FILE42\n' }] }
const payload = (nativeItem: unknown) => ({ threadId: sessionId, turnId: 'native-turn', item: nativeItem })
function storedMessage(id: string, seq: bigint, body: unknown): AgentChatMessage {
  return makeMessage({
    id,
    seq,
    source: MessageSource.AGENT,
    agentProvider: AgentProvider.CODEX,
    spanId: item.id,
    spanType: 'fileChange',
    agentSessionId: sessionId,
    // The Worker stores a native Codex item with no Worker completion.
    completion: MessageCompletion.UNSPECIFIED,
    content: rawContent(body),
  })
}
const started = storedMessage('native-started', 41n, payload({ ...item, status: 'inProgress', changes: [] }))
const completed = storedMessage('native-completed', 42n, payload(item))

// The reasons that the reader gives for a refusal.
const NOT_ONE_START_AND_COMPLETION = 'The Codex file item must have one actual start and one actual completion.'
const UNRELATED_ITEM = 'The exact Worker file span contains an unrelated native item.'
const INVALID_METADATA = 'The exact Worker file span contains invalid native message metadata.'
const COMPLETED_BEFORE_START = 'The native Codex file item completed before its actual start.'
const NOT_APPLIED = 'The native Codex file item did not report a completed applied change.'

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
    { label: 'empty span', error: NOT_ONE_START_AND_COMPLETION, messages: [] },
    { label: 'start only', error: NOT_ONE_START_AND_COMPLETION, messages: [started] },
    { label: 'completion only', error: NOT_ONE_START_AND_COMPLETION, messages: [completed] },
    { label: 'duplicate completion', error: NOT_ONE_START_AND_COMPLETION, messages: [started, completed, completed] },
    { label: 'wrong item ID', error: UNRELATED_ITEM, messages: [started, storedMessage('native-completed', 42n, payload({ ...item, id: 'another-item' }))] },
    { label: 'failed item', error: NOT_ONE_START_AND_COMPLETION, messages: [started, storedMessage('native-completed', 42n, payload({ ...item, status: 'failed' }))] },
    { label: 'wrong item type', error: UNRELATED_ITEM, messages: [started, storedMessage('native-completed', 42n, payload({ ...item, type: 'commandExecution' }))] },
    { label: 'wrong diff path', error: 'The native Codex completion contains no unique added-file diff for the exact path.', messages: [started, storedMessage('native-completed', 42n, payload({ ...item, changes: [{ path: '/another/file.txt', kind: 'add', diff: 'ACTUAL_FILE42\n' }] }))] },
  ])('refuses an absent, duplicated, failed, or unrelated applied native item: $label', ({ messages, error }) => {
    expect(() => codexAppliedFileChange(messages, sessionId, item.id, '/private/file.txt')).toThrow(error)
  })
  it.each([MessageCompletion.COMPLETE, MessageCompletion.INTERRUPTED, MessageCompletion.ERROR])('refuses a completed row that carries the Worker completion %s', (completion) => {
    const finishedByWorker = makeMessage({ ...completed, completion })
    expect(() => codexAppliedFileChange([started, finishedByWorker], sessionId, item.id, '/private/file.txt')).toThrow('did not report a completed applied change')
  })
  it('refuses a start row with sequence 0, which the Worker never allocates', () => {
    const zeroStart = makeMessage({ ...started, seq: 0n })
    const firstEnd = makeMessage({ ...completed, seq: 1n })
    expect(() => codexAppliedFileChange([zeroStart, firstEnd], sessionId, item.id, '/private/file.txt')).toThrow('invalid native message metadata')
  })
  it('refuses native completion before the same item starts', () => {
    const earlyCompleted = makeMessage({ ...completed, seq: 40n })
    expect(() => codexAppliedFileChange([earlyCompleted, started], sessionId, item.id, '/private/file.txt')).toThrow('before its actual start')
  })
  it.each([
    { label: 'empty message ID', error: INVALID_METADATA, fields: { id: '' } },
    { label: 'negative sequence', error: INVALID_METADATA, fields: { seq: -1n } },
    { label: 'sequence 0, which the Worker never allocates', error: INVALID_METADATA, fields: { seq: 0n } },
    { label: 'equal sequence', error: COMPLETED_BEFORE_START, fields: { seq: 41n } },
    { label: 'duplicate message ID', error: COMPLETED_BEFORE_START, fields: { id: started.id } },
    { label: 'user message source', error: INVALID_METADATA, fields: { source: MessageSource.USER } },
    { label: 'wrong provider', error: INVALID_METADATA, fields: { agentProvider: AgentProvider.DROID } },
    { label: 'wrong span ID', error: NOT_ONE_START_AND_COMPLETION, fields: { spanId: 'another-span' } },
    { label: 'wrong span type', error: INVALID_METADATA, fields: { spanType: 'commandExecution' } },
    { label: 'error completion', error: NOT_APPLIED, fields: { completion: MessageCompletion.ERROR } },
    { label: 'interrupted completion', error: NOT_APPLIED, fields: { completion: MessageCompletion.INTERRUPTED } },
  ])('refuses invalid Worker metadata on the exact native file result: $label', ({ fields, error }) => {
    expect(() => codexAppliedFileChange([started, makeMessage({ ...completed, ...fields })], sessionId, item.id, '/private/file.txt')).toThrow(error)
  })
  it.each([
    [{ body: { ...payload(item), threadId: 'another-thread' } }, UNRELATED_ITEM],
    [{ body: { ...payload(item), turnId: '' } }, UNRELATED_ITEM],
    [{ body: { ...payload(item), turnId: 'another-turn' } }, NOT_APPLIED],
    [{ body: { method: 'item/completed', params: payload(item) } }, UNRELATED_ITEM],
  ])('refuses unrelated thread, turn, or envelope data inside the stored params: %j', ({ body }, error) => {
    expect(() => codexAppliedFileChange([started, storedMessage('native-completed', 42n, body)], sessionId, item.id, '/private/file.txt')).toThrow(error)
  })
  it('excludes old-session spans while preserving the exact current session and full sequence values', () => {
    const old = [started, completed].map(message => makeMessage({ ...message, agentSessionId: 'old-session' }))
    const currentStart = makeMessage({ ...started, seq: 9223372036854775806n })
    const currentEnd = makeMessage({ ...completed, seq: 9223372036854775807n })
    expect(codexAppliedFileChange([...old, currentStart, currentEnd], sessionId, item.id, '/private/file.txt')).toEqual(item.changes[0])
  })
  it('preserves the first allocated sequence and an explicitly empty native diff', () => {
    const currentStart = makeMessage({ ...started, seq: 1n })
    const currentEnd = storedMessage('native-completed', 2n, payload({ ...item, changes: [{ path: '/private/file.txt', kind: 'add', diff: '' }] }))
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
  const UNFINISHED = 'The exact native Codex patch code cell did not finish successfully.'
  const NO_EMPTY_OBJECT = 'The exact native Codex patch call did not return its successful empty object.'
  it.each([
    [{ output: [] }, UNFINISHED],
    [{ output: [blocks[0]] }, NO_EMPTY_OBJECT],
    [{ output: [blocks[1]] }, UNFINISHED],
    [{ output: [...blocks, blocks[1]] }, NO_EMPTY_OBJECT],
    [{ output: [{ type: 'input_text', text: 'Script failed\n' }, blocks[1]] }, UNFINISHED],
  ])('refuses missing or unsuccessful native patch completion: %j', ({ output }, error) => {
    expect(() => requireCodexPatchResult(request(output), 'actual-patch')).toThrow(error)
  })
  it('refuses another call result even when its completion looks successful', () => {
    expect(() => requireCodexPatchResult(request(blocks, 'other-patch'), 'actual-patch')).toThrow('The native request contains 0 results for actual-patch.')
  })
})
