import type { MessageInitShape } from '@bufbuild/protobuf'
import type { Page } from '@playwright/test'
import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ModelScript } from '../helpers/modelScriptFixture'
import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { MUSE_ITEM_KIND, MUSE_METHOD, MUSE_STREAM_KIND } from '../../../src/generated/contracts/muse-protocol'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { museToolRowId, nativeContext } from './scenarios'

const query = { agentId: 'selected-child', callId: 'model-call' }

function row(options: { id?: string, revision?: number, item?: Record<string, unknown>, params?: Record<string, unknown>, message?: MessageInitShape<typeof AgentChatMessageSchema> } = {}): AgentChatMessage {
  const id = options.id ?? 'native-item'
  const revision = options.revision ?? 1
  const body = {
    jsonrpc: '2.0',
    method: MUSE_METHOD.ItemCompleted,
    params: {
      sessionId: 'child-session',
      sourceRange: { stream: { kind: MUSE_STREAM_KIND.Session, id: 'child-session' }, first: { id: 'origin', sequence: 7 }, last: { id: 'fold', sequence: 9 } },
      ...options.params,
      item: { itemId: id, revision, kind: MUSE_ITEM_KIND.ToolCall, turnId: 'native-turn', callId: query.callId, tool: 'bash', args: '{}', status: 'completed', ...options.item },
    },
  }
  return create(AgentChatMessageSchema, {
    id: `stored-${id}-${revision}`,
    agentProvider: AgentProvider.MUSE_CODE,
    source: MessageSource.AGENT,
    agentSessionId: 'child-session',
    spanId: id,
    content: new TextEncoder().encode(JSON.stringify(body)),
    contentCompression: ContentCompression.NONE,
    ...options.message,
  })
}

function snapshot(messages: AgentChatMessage[] = [row()]): NativeMessageSnapshot {
  return { agentId: query.agentId, agentSessionId: 'child-session', messages }
}

describe('nativeContext', () => {
  it('supplies the Muse resolver for native browser row identities', async () => {
    const context = await nativeContext({
      page: {} as Page,
      modelScript: {} as ModelScript,
      leapmuxServer: { hubUrl: 'http://127.0.0.1:1', adminToken: 'test', workerId: 'worker' },
      workspaceId: 'workspace',
    })
    expect(context.resolveToolRowId).toBeTypeOf('function')
  })
})

describe('museToolRowId', () => {
  it('returns the actual item ID of the exact selected child', () => {
    expect(museToolRowId(snapshot(), query)).toBe('native-item')
  })

  it('keeps one native lifecycle through higher revisions and exact replay', () => {
    const latest = row({ revision: 3, item: { turnId: 'later-native-turn' }, params: { sourceRange: { stream: { kind: MUSE_STREAM_KIND.Session, id: 'child-session' }, first: { id: 'later-origin', sequence: 11 } } } })
    expect(museToolRowId(snapshot([latest, row(), latest, row({ revision: 2 })]), query)).toBe('native-item')
  })

  it('refuses distinct native items that share the requested call ID', () => {
    expect(() => museToolRowId(snapshot([row(), row({ id: 'another-item' })]), query)).toThrow('exactly one item')
  })

  it.each(['', '  '])('refuses an absent model call ID: %j', (callId) => {
    expect(() => museToolRowId(snapshot(), { ...query, callId })).toThrow('exact agent')
  })

  it.each(['', 'another-agent'])('refuses another snapshot agent: %j', (agentId) => {
    expect(() => museToolRowId({ ...snapshot(), agentId }, query)).toThrow('exact agent')
  })

  it.each([0, -1, 0.5, Number.MAX_SAFE_INTEGER + 1])('refuses an invalid native revision: %s', (revision) => {
    expect(() => museToolRowId(snapshot([row({ revision })]), query)).toThrow('item lifecycle')
  })

  it('preserves a very large valid revision', () => {
    expect(museToolRowId(snapshot([row({ revision: Number.MAX_SAFE_INTEGER })]), query)).toBe('native-item')
  })

  it.each([
    { agentSessionId: 'parent-session' },
    { agentProvider: AgentProvider.CODEX },
    { source: MessageSource.USER },
  ])('rejects a row outside the native Worker ownership: %j', (message) => {
    expect(() => museToolRowId(snapshot([row({ message })]), query)).toThrow('exactly one item')
  })

  it('refuses a Worker span that differs from the actual item ID', () => {
    expect(() => museToolRowId(snapshot([row({ message: { spanId: 'model-call' } })]), query)).toThrow('exact span')
  })

  it.each([{}, { id: 'origin', sequence: 0 }, { id: 'origin', sequence: -1 }, { id: 'origin', sequence: 1.5 }])('refuses an absent or invalid origin position: %j', (first) => {
    expect(() => museToolRowId(snapshot([row({ params: { sourceRange: { stream: { kind: MUSE_STREAM_KIND.Session, id: 'child-session' }, first } } })]), query)).toThrow('origin identity')
  })

  it('rejects a foreign native frame session', () => {
    expect(() => museToolRowId(snapshot([row({ params: { sessionId: 'parent-session' } })]), query)).toThrow('exactly one item')
  })

  it('preserves a stored native JSON decode failure', () => {
    expect(() => museToolRowId(snapshot([row({ message: { content: new TextEncoder().encode('invalid JSON') } })]), query)).toThrow('invalid JSON')
  })
})
