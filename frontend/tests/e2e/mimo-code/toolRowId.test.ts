import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentChatMessageSchema, AgentInfoSchema, AgentProvider, AgentStatus, ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { nativeAgentById } from '../helpers/nativeScenario'
import { mimoToolRowIdResolver, readMiMoNativeToolRowId, requireMiMoChildScope } from './toolRowId'

vi.mock('../helpers/nativeScenario', async importOriginal => ({
  ...await importOriginal<typeof import('../helpers/nativeScenario')>(),
  nativeAgentById: vi.fn(),
}))
vi.mock('../helpers/nativeMessages', async importOriginal => ({
  ...await importOriginal<typeof import('../helpers/nativeMessages')>(),
  readNativeMessageSnapshot: vi.fn(),
}))

const nativeSessionId = 'native-session'
const callId = 'model-call'
const partId = 'native-part'
const encoder = new TextEncoder()

function frame(change: Record<string, unknown> = {}, outerSession?: unknown): Record<string, unknown> {
  return {
    type: 'message.part.updated',
    properties: { ...(outerSession === undefined ? {} : { sessionID: outerSession }), part: { id: partId, messageID: 'native-message', sessionID: nativeSessionId, callID: callId, type: 'tool', tool: 'read', state: { status: 'completed', input: { filePath: '/private/file' }, output: 'The actual file output.' }, ...change } },
  }
}

function message(body: unknown, change: Partial<Pick<AgentChatMessage, 'id' | 'spanId' | 'agentSessionId'>> = {}): AgentChatMessage {
  return create(AgentChatMessageSchema, { id: 'stored-row', spanId: partId, agentSessionId: nativeSessionId, content: encoder.encode(JSON.stringify(body)), contentCompression: ContentCompression.NONE, ...change })
}

function snapshot(messages = [message(frame())], change: Partial<NativeMessageSnapshot> = {}): NativeMessageSnapshot {
  return { agentId: 'root', agentSessionId: nativeSessionId, messages, ...change }
}

const owner = { agentId: 'root', nativeSessionId }
const root = create(AgentInfoSchema, { id: 'root', status: AgentStatus.ACTIVE, agentProvider: AgentProvider.MIMO_CODE, agentSessionId: nativeSessionId, rootAgentId: 'root' })
const child = create(AgentInfoSchema, { id: 'child', status: AgentStatus.ACTIVE, agentProvider: AgentProvider.MIMO_CODE, agentSessionId: nativeSessionId, parentAgentId: root.id, rootAgentId: root.id, providerChildKey: 'spawn-part', spawnSpanId: 'spawn-part' })

function spawnSnapshot(metadata: unknown = { actorId: 'native-actor' }, outerSession?: unknown): NativeMessageSnapshot {
  return snapshot([message(frame({ id: 'spawn-part', tool: 'actor', callID: 'model-spawn', state: { status: 'running', input: { operation: { action: 'run', prompt: 'Read the assigned file.' } }, metadata } }, outerSession), { spanId: 'spawn-part' })])
}

beforeEach(() => {
  vi.mocked(nativeAgentById).mockReset()
  vi.mocked(readNativeMessageSnapshot).mockReset()
})

describe('readMiMoNativeToolRowId', () => {
  it('reads the actual native part and keeps the model ID separate', () => {
    const stored = snapshot()
    expect(readMiMoNativeToolRowId(stored, callId, owner)).toBe(partId)
    expect(stored.messages[0]?.spanId).toBe(partId)
    expect(callId).not.toBe(partId)
  })

  it('accepts repeated updates of one part in one native message', () => {
    const opening = message(frame({ state: { status: 'running', input: { filePath: '/private/file' } } }))
    const result = message(frame(), { id: 'closing-row' })
    expect(readMiMoNativeToolRowId(snapshot([opening, result]), callId, owner)).toBe(partId)
  })

  it('refuses model ID reuse across two distinct parts in the same transcript', () => {
    const another = message(frame({ id: 'second-part' }), { id: 'second-row', spanId: 'second-part' })
    expect(() => readMiMoNativeToolRowId(snapshot([message(frame()), another]), callId, owner)).toThrow('matches 2 distinct')
  })

  it('refuses a part that changes its native message identity', () => {
    expect(() => readMiMoNativeToolRowId(snapshot([message(frame()), message(frame({ messageID: 'foreign-message' }))]), callId, owner)).toThrow('conflicting native messages')
  })

  it.each([
    { label: 'stored session', row: message(frame(), { agentSessionId: 'foreign-session' }) },
    { label: 'native session', row: message(frame({ sessionID: 'foreign-session' })) },
    { label: 'model call', row: message(frame({ callID: 'foreign-call' })) },
    { label: 'native type', row: message(frame({ type: 'text' })) },
  ])('refuses a foreign $label', ({ row }) => {
    expect(() => readMiMoNativeToolRowId(snapshot([row]), callId, owner)).toThrow('matches 0 distinct')
  })

  it.each([
    { label: 'span', row: message(frame(), { spanId: 'foreign-part' }), reason: 'span does not match' },
    { label: 'message', row: message(frame({ messageID: '' })), reason: 'no native part or message identity' },
    { label: 'status', row: message(frame({ state: { status: 'invented' } })), reason: 'unknown status' },
  ])('refuses invalid $label identity', ({ row, reason }) => {
    expect(() => readMiMoNativeToolRowId(snapshot([row]), callId, owner)).toThrow(reason)
  })

  it.each([{}, { type: 'message.part.updated', properties: null }, { type: 'message.part.updated', properties: { part: null } }])('refuses a missing native tool part: %j', (body) => {
    expect(() => readMiMoNativeToolRowId(snapshot([message(body)]), callId, owner)).toThrow('matches 0 distinct')
  })

  it.each([{ agentId: 'foreign-agent' }, { agentSessionId: 'foreign-session' }])('refuses another snapshot owner: %j', (change) => {
    expect(() => readMiMoNativeToolRowId(snapshot(undefined, change), callId, owner)).toThrow('another Worker owner')
  })

  it('reads a virtual child only in its selected Worker transcript', () => {
    const stored = snapshot(undefined, { agentId: child.id })
    expect(readMiMoNativeToolRowId(stored, callId, { agentId: child.id, nativeSessionId })).toBe(partId)
    expect(() => readMiMoNativeToolRowId(stored, callId, owner)).toThrow('another Worker owner')
  })

  it('ignores an older stored session when the current exact part exists', () => {
    const older = message(frame({ sessionID: 'older-session', id: 'older-part' }), { agentSessionId: 'older-session', spanId: 'older-part' })
    expect(readMiMoNativeToolRowId(snapshot([older, message(frame())]), callId, owner)).toBe(partId)
  })

  it('refuses an incomplete part of the queried call even when another valid part exists', () => {
    const incomplete = message(frame({ id: '' }), { id: 'incomplete-row', spanId: '' })
    expect(() => readMiMoNativeToolRowId(snapshot([message(frame()), incomplete]), callId, owner)).toThrow('queried native tool part is incomplete')
  })

  it('does not use an incomplete part of an unrelated model call', () => {
    const unrelated = message(frame({ id: '', callID: 'unrelated-call' }), { id: 'unrelated-row', spanId: '' })
    expect(readMiMoNativeToolRowId(snapshot([unrelated, message(frame())]), callId, owner)).toBe(partId)
  })

  it.each(['foreign-session', '', null, 0])('refuses contradictory outer ownership beside a valid queried part: %j', (outerSession) => {
    const contradictory = message(frame({}, outerSession), { id: 'contradictory-row' })
    expect(() => readMiMoNativeToolRowId(snapshot([message(frame()), contradictory]), callId, owner)).toThrow('conflicting outer session ownership')
  })

  it('accepts an exact optional outer session and ignores an unrelated foreign call', () => {
    const unrelated = message(frame({ callID: 'unrelated-call' }, 'foreign-session'), { id: 'unrelated-row' })
    expect(readMiMoNativeToolRowId(snapshot([unrelated, message(frame({}, nativeSessionId))]), callId, owner)).toBe(partId)
  })
})

describe('requireMiMoChildScope', () => {
  it('validates the durable child links against the original parent native spawn', () => {
    expect(() => requireMiMoChildScope(child, root, spawnSnapshot())).not.toThrow()
  })

  it('accepts a held foreground opening that has no later actor metadata', () => {
    expect(() => requireMiMoChildScope(child, root, spawnSnapshot({}))).not.toThrow()
    const withoutMetadata = frame({ id: 'spawn-part', tool: 'actor', callID: 'model-spawn', state: { status: 'running', input: { operation: { action: 'run', prompt: 'Read the assigned file.' } } } })
    expect(() => requireMiMoChildScope(child, root, snapshot([message(withoutMetadata, { spanId: 'spawn-part' })]))).not.toThrow()
  })

  it('validates the original child spawn after its parent changes native session', () => {
    const currentParent = create(AgentInfoSchema, { ...root, agentSessionId: 'replacement-session' })
    const parentRows = snapshot(spawnSnapshot().messages, { agentSessionId: currentParent.agentSessionId })
    expect(() => requireMiMoChildScope(child, currentParent, parentRows)).not.toThrow()
  })

  it('validates the captured fallback session after its parent changes native session', () => {
    const currentParent = create(AgentInfoSchema, { ...root, agentSessionId: 'replacement-session' })
    const key = `${nativeSessionId}/native-actor`
    const originalChild = create(AgentInfoSchema, { ...child, providerChildKey: key, spawnSpanId: key })
    expect(() => requireMiMoChildScope(originalChild, currentParent, snapshot([], { agentSessionId: currentParent.agentSessionId }))).not.toThrow()
  })

  it('refuses a child without its captured native session', () => {
    const unknownChild = create(AgentInfoSchema, { ...child, agentSessionId: '' })
    expect(() => requireMiMoChildScope(unknownChild, root, spawnSnapshot())).toThrow('exact durable parent and native session owner')
  })

  it('refuses a replacement-session spawn for an original-session child', () => {
    const replacementSpawn = message(frame({ id: 'spawn-part', sessionID: 'replacement-session', tool: 'actor', callID: 'model-spawn', state: { status: 'running', input: { operation: { action: 'run', prompt: 'Read the assigned file.' } } } }), { agentSessionId: 'replacement-session', spanId: 'spawn-part' })
    const currentParent = create(AgentInfoSchema, { ...root, agentSessionId: 'replacement-session' })
    expect(() => requireMiMoChildScope(child, currentParent, snapshot([replacementSpawn], { agentSessionId: currentParent.agentSessionId }))).toThrow('no original spawn frame')
  })

  it('validates the session and actor fallback without a guessed spawn part', () => {
    const key = `${nativeSessionId}/native-actor`
    expect(() => requireMiMoChildScope(create(AgentInfoSchema, { ...child, providerChildKey: key, spawnSpanId: key }), root, snapshot([]))).not.toThrow()
  })

  it.each(['', 'main', null, 0])('refuses a present invalid native actor ID: %j', (actorId) => {
    expect(() => requireMiMoChildScope(child, root, spawnSnapshot({ actorId }))).toThrow('invalid or conflicting native actor ID')
  })

  it('refuses actor metadata that conflicts between native updates of the spawn', () => {
    const initial = spawnSnapshot({ actorId: 'first-actor' }).messages
    const later = spawnSnapshot({ actorId: 'foreign-actor' }).messages
    expect(() => requireMiMoChildScope(child, root, snapshot([...initial, ...later]))).toThrow('invalid or conflicting native actor ID')
  })

  it.each([
    { change: { parentAgentId: 'foreign-parent' }, reason: 'exact durable parent' },
    { change: { rootAgentId: 'foreign-root' }, reason: 'exact durable parent' },
    { change: { spawnSpanId: 'foreign-span' }, reason: 'exact native spawn part link' },
    { change: { providerChildKey: 'foreign-part' }, reason: 'exact native spawn part link' },
  ])('refuses changed durable child links: %j', ({ change, reason }) => {
    expect(() => requireMiMoChildScope(create(AgentInfoSchema, { ...child, ...change }), root, spawnSnapshot())).toThrow(reason)
  })

  it('refuses a spawn from another native session', () => {
    expect(() => requireMiMoChildScope(child, root, snapshot(spawnSnapshot().messages, { agentSessionId: 'foreign-session' }))).toThrow('exact durable parent')
  })

  it('refuses a foreign-session fallback key', () => {
    const key = 'foreign-session/native-actor'
    expect(() => requireMiMoChildScope(create(AgentInfoSchema, { ...child, providerChildKey: key, spawnSpanId: key }), root, snapshot([]))).toThrow('no original spawn frame')
  })

  it.each(['foreign-session', '', null, 0])('refuses contradictory outer ownership of the original parent spawn: %j', (outerSession) => {
    const contradictory = spawnSnapshot({}, outerSession)
    expect(() => requireMiMoChildScope(child, root, snapshot([...spawnSnapshot().messages, ...contradictory.messages]))).toThrow('conflicting outer session ownership')
  })

  it('accepts an original parent spawn with the exact optional outer session', () => {
    expect(() => requireMiMoChildScope(child, root, spawnSnapshot({}, nativeSessionId))).not.toThrow()
  })

  it('refuses an incomplete original spawn update beside valid parent evidence', () => {
    const incomplete = message(frame({ id: 'spawn-part', tool: 'actor', callID: 'model-spawn', state: null }), { id: 'incomplete-spawn', spanId: 'spawn-part' })
    expect(() => requireMiMoChildScope(child, root, snapshot([...spawnSnapshot().messages, incomplete]))).toThrow('original native actor spawn is incomplete')
  })
})

describe('mimoToolRowIdResolver', () => {
  const context = { leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'worker' } }

  it('resolves the actual selected root frames without reading another transcript', async () => {
    vi.mocked(nativeAgentById).mockResolvedValue(root)
    vi.mocked(readNativeMessageSnapshot).mockResolvedValue(snapshot())
    expect(await mimoToolRowIdResolver(context)({ callId, agentId: root.id })).toBe(partId)
    expect(readNativeMessageSnapshot).toHaveBeenCalledExactlyOnceWith(context, root.id)
  })

  it('selects the held child frames and uses the parent only to validate ownership', async () => {
    vi.mocked(nativeAgentById).mockImplementation(async (_context, id) => id === root.id ? root : child)
    vi.mocked(readNativeMessageSnapshot).mockImplementation(async (_context, id) => id === root.id ? spawnSnapshot({}) : snapshot(undefined, { agentId: child.id }))
    expect(await mimoToolRowIdResolver(context)({ callId, agentId: child.id })).toBe(partId)
    expect(vi.mocked(readNativeMessageSnapshot).mock.calls.map(call => call[1])).toEqual([root.id, child.id])
  })

  it('refuses a root native session that changes during child resolution', async () => {
    let rootReads = 0
    vi.mocked(nativeAgentById).mockImplementation(async (_context, id) => {
      if (id !== root.id)
        return child
      rootReads++
      return rootReads === 1 ? root : create(AgentInfoSchema, { ...root, agentSessionId: 'changed-session' })
    })
    vi.mocked(readNativeMessageSnapshot).mockImplementation(async (_context, id) => id === root.id ? spawnSnapshot() : snapshot(undefined, { agentId: child.id }))
    await expect(mimoToolRowIdResolver(context)({ callId, agentId: child.id })).rejects.toThrow('parent owner changed')
  })

  it('selects captured child rows when its parent uses a replacement session and reuses the model call', async () => {
    const currentParent = create(AgentInfoSchema, { ...root, agentSessionId: 'replacement-session' })
    const replacementPart = message(frame({ id: 'replacement-part', sessionID: currentParent.agentSessionId }), { id: 'replacement-row', agentSessionId: currentParent.agentSessionId, spanId: 'replacement-part' })
    vi.mocked(nativeAgentById).mockImplementation(async (_context, id) => id === root.id ? currentParent : child)
    vi.mocked(readNativeMessageSnapshot).mockImplementation(async (_context, id) => id === root.id
      ? snapshot(spawnSnapshot().messages, { agentSessionId: currentParent.agentSessionId })
      : snapshot([message(frame()), replacementPart], { agentId: child.id }))
    expect(await mimoToolRowIdResolver(context)({ callId, agentId: child.id })).toBe(partId)
    expect(vi.mocked(readNativeMessageSnapshot).mock.calls.map(call => call[1])).toEqual([root.id, child.id])
  })

  it('refuses a child native session that changes during row resolution', async () => {
    let childReads = 0
    vi.mocked(nativeAgentById).mockImplementation(async (_context, id) => {
      if (id === root.id)
        return root
      childReads++
      return childReads === 1 ? child : create(AgentInfoSchema, { ...child, agentSessionId: 'replacement-session' })
    })
    vi.mocked(readNativeMessageSnapshot).mockImplementation(async (_context, id) => id === root.id ? spawnSnapshot() : snapshot(undefined, { agentId: child.id }))
    await expect(mimoToolRowIdResolver(context)({ callId, agentId: child.id })).rejects.toThrow('selected transcript owner changed')
  })

  it('preserves a failed Worker snapshot read', async () => {
    const failure = new Error('The Worker denied the transcript read.')
    vi.mocked(nativeAgentById).mockResolvedValue(root)
    vi.mocked(readNativeMessageSnapshot).mockRejectedValue(failure)
    await expect(mimoToolRowIdResolver(context)({ callId, agentId: root.id })).rejects.toBe(failure)
  })
})
