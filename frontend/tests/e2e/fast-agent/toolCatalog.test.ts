import type { MockModelScenarioStatus } from '../helpers/mockModelScript'
import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { FastAgentCompleteCatalog } from './toolCatalog'
import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ASSEMBLED_MESSAGE } from '../../../src/generated/contracts/worker-vocab'
import { AgentChatMessageSchema, AgentInputKind, ContentCompression, EnqueueAgentInputRequestSchema, EnqueueAgentInputResponseSchema, MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { assertFastAgentCatalogNoInference, assertFastAgentShellCatalog, fastAgentCatalogCommandReply, parseFastAgentCatalog, parseFastAgentToolSchema, sendFastAgentCatalogCommand } from './toolCatalog'

const calls = vi.hoisted(() => ({ channel: vi.fn(), enqueue: vi.fn() }))
vi.mock('../helpers/api', () => ({ getTestChannel: calls.channel }))

beforeEach(() => {
  vi.resetAllMocks()
  calls.channel.mockResolvedValue({ callWorker: calls.enqueue })
  calls.enqueue.mockResolvedValue(create(EnqueueAgentInputResponseSchema, { snapshot: { agentId: 'native-agent' } }))
})

const listing = '# tools\n\n## MCP / local tools\n\n1. **execute**\n    > Execute a shell command.\n    > **Args:** `command`\n\n2. **read\\_text\\_file**\n    > Read content from a text file.\n    > Returns the file contents.\n'

describe('sendFastAgentCatalogCommand', () => {
  const server = { hubUrl: 'http://unit.invalid', adminToken: 'private-token', workerId: 'actual-worker' }

  it('sends exact underscore and Unicode arguments through the actual typed Worker user-input route', async () => {
    await sendFastAgentCatalogCommand(server, 'native-agent', '/tools read_text_file_한글')
    expect(calls.channel).toHaveBeenCalledExactlyOnceWith(server.hubUrl, server.adminToken)
    expect(calls.enqueue).toHaveBeenCalledExactlyOnceWith('actual-worker', 'EnqueueAgentInput', EnqueueAgentInputRequestSchema, EnqueueAgentInputResponseSchema, {
      agentId: 'native-agent',
      inputId: expect.stringMatching(/^[0-9a-f-]{36}$/),
      text: '/tools read_text_file_한글',
      attachments: [],
      kind: AgentInputKind.USER_MESSAGE,
    })
  })

  it.each([
    { agentId: '', command: '/tools' },
    { agentId: 'native-agent', command: '/tools2' },
    { agentId: 'native-agent', command: '/tools read\nother' },
  ])('rejects an absent identity or unrelated native command: %j', async ({ agentId, command }) => {
    await expect(sendFastAgentCatalogCommand(server, agentId, command)).rejects.toThrow('exact agent')
    expect(calls.enqueue).not.toHaveBeenCalled()
  })

  it.each([
    create(EnqueueAgentInputResponseSchema),
    create(EnqueueAgentInputResponseSchema, { snapshot: { agentId: 'another-agent' } }),
    create(EnqueueAgentInputResponseSchema, { snapshot: { agentId: 'native-agent', paused: true } }),
  ])('rejects an absent, unrelated, or paused Worker queue', async (response) => {
    calls.enqueue.mockResolvedValue(response)
    await expect(sendFastAgentCatalogCommand(server, 'native-agent', '/tools')).rejects.toThrow('active queue')
  })

  it('retains the actual Worker transport failure', async () => {
    const cause = new Error('The actual Worker metadata queue failed.')
    calls.enqueue.mockRejectedValue(cause)
    await expect(sendFastAgentCatalogCommand(server, 'native-agent', '/tools')).rejects.toBe(cause)
  })
})

function schemaReply(name: string, schema: unknown): string {
  const escaped = name.replace(/[\\[\]*_`]/g, '\\$&')
  return `# Tool schema: ${escaped}\n\n## Input schema\n\n\`\`\`json\n${JSON.stringify(schema, null, 2)}\n\`\`\``
}

describe('parseFastAgentCatalog', () => {
  it('reads the complete sequential list and wrapped descriptions without argument metadata', () => {
    expect(parseFastAgentCatalog(listing)).toEqual({
      tools: [{ name: 'execute', description: 'Execute a shell command.' }, { name: 'read_text_file', description: 'Read content from a text file.\nReturns the file contents.' }],
      hosted: [],
    })
  })

  it('preserves Unicode and exact Markdown escapes and retains hosted tools separately', () => {
    const text = `${listing}\n## Provider-managed / hosted tools\n\n- **native\\_출력** _(enabled)_ — Hosted output.\n`
    expect(parseFastAgentCatalog(text).hosted).toEqual(['native_출력'])
    expect(parseFastAgentCatalog(listing.replace('**execute**', '**execute-code.v2**')).tools[0]?.name).toBe('execute-code.v2')
  })

  it.each([
    ['', 'headings'],
    ['# tools\n\nNo tools available for this agent.', 'headings'],
    [listing.replace('2. **', '3. **'), 'sequence'],
    [listing.replace('1. **', '0. **'), 'sequence'],
    [listing.replace('2. **read\\_text\\_file**', '2. read_text_file'), 'malformed'],
    [listing.replace('read\\_text\\_file', 'execute'), 'repeats'],
    [listing.replace('    > Execute a shell command.\n', ''), 'complete'],
    [listing.replace('## MCP / local tools', '## unknown'), 'headings'],
    [`${listing}\n## MCP / local tools\n`, 'repeated'],
    [`${listing}\n## unknown\n`, 'unknown'],
    [`${listing}\n## Provider-managed / hosted tools\n- wrong\n`, 'malformed'],
    [`${listing}\n## Provider-managed / hosted tools\n- **same**\n- **same**\n`, 'repeats'],
  ])('refuses an incomplete native list: %j', (text, reason) => {
    expect(() => parseFastAgentCatalog(text)).toThrow(reason)
  })
})

describe('parseFastAgentToolSchema', () => {
  it('reads the exact tool schema and ignores a separate structured output schema', () => {
    const input = { type: 'object', properties: { command: { type: 'string' } }, required: ['command'] }
    const output = `${schemaReply('native_출력-code.v2', input)}\n\n## Structured output schema\n\n\`\`\`json\n{"type":"string"}\n\`\`\``
    expect(parseFastAgentToolSchema(output, 'native_출력-code.v2')).toEqual(input)
  })

  it('accepts an object schema with no declared properties', () => {
    expect(parseFastAgentToolSchema(schemaReply('empty', { type: 'object' }), 'empty')).toEqual({ type: 'object' })
  })

  it.each([null, [], {}, { type: 'array' }, { type: 'object', properties: null }])('refuses an invalid native schema: %j', (schema) => {
    expect(() => parseFastAgentToolSchema(schemaReply('execute', schema), 'execute')).toThrow('object schema')
  })

  it('refuses the schema of another tool or an absent tool identity', () => {
    const reply = schemaReply('other', { type: 'object' })
    expect(() => parseFastAgentToolSchema(reply, 'execute')).toThrow('another tool')
    expect(() => parseFastAgentToolSchema(reply, '')).toThrow('another tool')
  })

  it('refuses missing, duplicate, and malformed input schemas', () => {
    const reply = schemaReply('execute', { type: 'object' })
    expect(() => parseFastAgentToolSchema('# Tool schema: execute\n', 'execute')).toThrow('unique')
    expect(() => parseFastAgentToolSchema(`${reply}\n${reply}`, 'execute')).toThrow('unique')
    expect(() => parseFastAgentToolSchema(reply.replace('{\n  "type": "object"\n}', '{broken'), 'execute')).toThrow()
  })
})

function nativeMessage(seq: bigint, body: unknown, source = MessageSource.AGENT) {
  return create(AgentChatMessageSchema, {
    id: `row-${seq}`,
    seq,
    source,
    agentSessionId: 'native-session',
    content: new TextEncoder().encode(JSON.stringify(body)),
    contentCompression: ContentCompression.NONE,
  })
}

function snapshot(messages: NativeMessageSnapshot['messages'] = []): NativeMessageSnapshot {
  return { agentId: 'actual-agent', agentSessionId: 'native-session', messages }
}

const assembled = { type: ASSEMBLED_MESSAGE.Type, kind: ASSEMBLED_MESSAGE.KindText, completion: ASSEMBLED_MESSAGE.CompletionComplete, text: listing }

describe('fastAgentCatalogCommandReply', () => {
  it('uses only the new completed native response after the exact Worker cursor', () => {
    const earlier = nativeMessage(0n, assembled)
    const before = snapshot([earlier])
    const user = nativeMessage(1n, assembled, MessageSource.USER)
    const incomplete = nativeMessage(2n, { ...assembled, completion: ASSEMBLED_MESSAGE.CompletionInterrupted })
    const reasoning = nativeMessage(3n, { ...assembled, kind: ASSEMBLED_MESSAGE.KindReasoning })
    expect(fastAgentCatalogCommandReply(before, snapshot([earlier, user, incomplete, reasoning]), '# tools\n')).toBeNull()
    expect(fastAgentCatalogCommandReply(before, snapshot([earlier, user, incomplete, reasoning, nativeMessage(4n, assembled)]), '# tools\n')).toBe(listing)
  })

  it('accepts sequence zero in an initially empty history', () => {
    expect(fastAgentCatalogCommandReply(snapshot(), snapshot([nativeMessage(0n, assembled)]), '# tools\n')).toBe(listing)
  })

  it('ignores a new Worker row from another native session', () => {
    const stale = nativeMessage(7n, assembled)
    stale.agentSessionId = 'older-native-session'
    expect(fastAgentCatalogCommandReply(snapshot(), snapshot([stale]), '# tools\n')).toBeNull()
  })

  it.each([
    { agentId: 'another-agent' },
    { agentSessionId: 'another-session' },
  ])('refuses a changed native identity: %j', (change) => {
    expect(() => fastAgentCatalogCommandReply(snapshot(), { ...snapshot([nativeMessage(0n, assembled)]), ...change }, '# tools\n')).toThrow('identity changed')
  })

  it('refuses an empty native session and duplicate matching responses', () => {
    expect(() => fastAgentCatalogCommandReply({ ...snapshot(), agentSessionId: '' }, snapshot(), '# tools\n')).toThrow('identity')
    expect(() => fastAgentCatalogCommandReply(snapshot(), snapshot([nativeMessage(0n, assembled), nativeMessage(1n, assembled)]), '# tools\n')).toThrow('ambiguous')
  })
})

function status(): MockModelScenarioStatus {
  return { complete: true, nextStep: 1, stepCount: 1, ruleMatches: {}, pendingGates: [], requests: [], unexpectedRequests: [] }
}

describe('assertFastAgentCatalogNoInference', () => {
  it('accepts an unchanged model scenario', () => {
    expect(() => assertFastAgentCatalogNoInference(status(), status())).not.toThrow()
  })

  it.each([
    { nextStep: 2 },
    { stepCount: 2 },
    { ruleMatches: { native: 1 } },
    { pendingGates: ['native'] },
  ])('rejects changed model scenario counters: %j', (change) => {
    expect(() => assertFastAgentCatalogNoInference(status(), { ...status(), ...change })).toThrow('model inference')
  })

  it('rejects a fallback request and an unexpected request without a queued-step change', () => {
    const after = status()
    after.requests.push({ protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: {}, fallback: true })
    expect(() => assertFastAgentCatalogNoInference(status(), after)).toThrow('model inference')
    const unexpected = status()
    unexpected.unexpectedRequests.push({ protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: {}, reason: 'Unscripted native request.' })
    expect(() => assertFastAgentCatalogNoInference(status(), unexpected)).toThrow('model inference')
  })
})

function shellCatalog(): FastAgentCompleteCatalog {
  const fields = {
    execute: ['command', 'args', 'env', 'cwd'],
    read_text_file: ['path', 'line', 'limit'],
    write_text_file: ['path', 'content'],
    subagent: ['message', 'model', 'label', 'include_user_message'],
  }
  return {
    tools: Object.entries(fields).map(([name, properties]) => ({ name, description: name === 'execute' ? 'Execute a shell command.' : 'Native tool.', schema: { type: 'object', properties: Object.fromEntries(properties.map(field => [field, {}])) } })),
    hosted: [],
  }
}

describe('assertFastAgentShellCatalog', () => {
  it('accepts only the complete audited shell, filesystem, and subagent inventory', () => {
    expect(() => assertFastAgentShellCatalog(shellCatalog())).not.toThrow()
  })

  it('rejects an executor under an arbitrary name and a code input added to an existing native tool', () => {
    const added = shellCatalog()
    added.tools.push({ name: 'native-runner', description: 'Run arbitrary JavaScript.', schema: { type: 'object', properties: { code: { type: 'string' } } } })
    expect(() => assertFastAgentShellCatalog(added)).toThrow('audited')
    const replaced = shellCatalog()
    replaced.tools[0]!.schema.properties = { code: { type: 'string' } }
    expect(() => assertFastAgentShellCatalog(replaced)).toThrow('unaudited')
  })

  it('rejects missing, repeated, and hosted native capabilities', () => {
    const missing = shellCatalog()
    missing.tools.pop()
    expect(() => assertFastAgentShellCatalog(missing)).toThrow('audited')
    const repeated = shellCatalog()
    repeated.tools[1] = repeated.tools[0]!
    expect(() => assertFastAgentShellCatalog(repeated)).toThrow('unaudited')
    expect(() => assertFastAgentShellCatalog({ ...shellCatalog(), hosted: ['code_interpreter'] })).toThrow('audited')
  })

  it('rejects a changed native executor description even when its input fields stay unchanged', () => {
    const changed = shellCatalog()
    changed.tools[0]!.description = 'Execute JavaScript source.'
    expect(() => assertFastAgentShellCatalog(changed)).toThrow('shell behavior')
  })
})
