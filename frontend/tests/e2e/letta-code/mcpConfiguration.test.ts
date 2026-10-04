import { Buffer } from 'node:buffer'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { configureLettaMcp, lettaConversationPath, parseLettaConversation, restoreLettaMcpSettings, setLettaMcpSettings } from './mcpConfiguration'

let directory: string
const identity = { agentId: 'agent-native', conversationId: 'conversation-native', baseUrl: 'local:/private/backend' }
const server = { name: 'echo_probe', transport: 'stdio' as const, command: resolve('node'), args: ['private script.mjs', ''] }

beforeEach(() => {
  const scratch = resolve(process.cwd(), '../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'letta-mcp-configuration-'))
})
afterEach(() => rmSync(directory, { recursive: true, force: true }))

describe('lettaConversationPath', () => {
  it('uses the native unpadded base64url key for the exact conversation', () => {
    const conversationId = 'conversation:한글/quote"'
    const backend = join(directory, 'backend')
    const path = lettaConversationPath(backend, conversationId)
    expect(path).toBe(join(backend, 'conversations', Buffer.from(`conversation:${conversationId}`).toString('base64url'), 'conversation.json'))
    expect(parseLettaConversation({ id: conversationId, agent_id: 'actual-agent' }, conversationId, backend))
      .toEqual({ conversationId, agentId: 'actual-agent', baseUrl: `local:${backend}` })
  })
  it.each(['', ' ', 'default', '\nconversation-native', 'conversation-native '])('rejects an invalid conversation identity: %j', (id) => {
    expect(() => lettaConversationPath(directory, id)).toThrow('conversation ID')
  })
  it('rejects a relative backend directory', () => {
    expect(() => lettaConversationPath('relative', 'conversation-native')).toThrow('must be absolute')
  })
})

describe('parseLettaConversation', () => {
  it.each([undefined, null, [], '', { id: 'other', agent_id: 'agent-native' }, { id: 'conversation-native' }])('rejects an invalid native conversation record: %j', (value) => {
    expect(() => parseLettaConversation(value, 'conversation-native', directory)).toThrow()
  })
  it.each(['', ' ', '\nagent-native', 'agent-native '])('rejects an empty or altered native agent ID: %j', (agent_id) => {
    expect(() => parseLettaConversation({ id: 'conversation-native', agent_id }, 'conversation-native', directory)).toThrow('identify its agent')
  })
})

describe('setLettaMcpSettings', () => {
  it('updates the exact agent and backend without changing other fields or entries', () => {
    const value = {
      profile: { effort: false },
      agents: [
        { agentId: identity.agentId, baseUrl: 'local:/other/backend', mcpServers: ['other-backend'] },
        { agentId: 'other-agent', baseUrl: identity.baseUrl, mcpServers: ['other-agent'] },
        { agentId: identity.agentId, baseUrl: identity.baseUrl, model: '', mcpServers: [] },
      ],
    }
    const changed = setLettaMcpSettings(value, identity, [server])
    expect(changed.settings).toEqual({ ...value, agents: [value.agents[0], value.agents[1], { ...value.agents[2], mcpServers: [server] }] })
    expect(value.agents[2]?.mcpServers).toEqual([])
    expect(changed.previous).toEqual({ kind: 'field-present', value: [] })
    expect(restoreLettaMcpSettings(changed.settings, identity, changed.previous)).toEqual(value)
  })
  it.each([{}, { agents: [] }])('creates an absent exact entry and removes only that entry on restoration: %j', (value) => {
    const changed = setLettaMcpSettings(value, identity, [])
    expect(changed.previous).toEqual({ kind: 'entry-absent' })
    expect(changed.settings.agents).toEqual([{ agentId: identity.agentId, baseUrl: identity.baseUrl, mcpServers: [] }])
    expect(restoreLettaMcpSettings(changed.settings, identity, changed.previous).agents).toEqual([])
  })
  it('restores an absent field and preserves newer unrelated native settings', () => {
    const value = { agents: [{ agentId: identity.agentId, baseUrl: identity.baseUrl, model: 'original' }] }
    const changed = setLettaMcpSettings(value, identity, [server])
    const newer = { profile: { fast: false }, agents: [{ ...changed.settings.agents[0], model: 'newer', extra: 0 }] }
    expect(restoreLettaMcpSettings(newer, identity, changed.previous))
      .toEqual({ profile: { fast: false }, agents: [{ agentId: identity.agentId, baseUrl: identity.baseUrl, model: 'newer', extra: 0 }] })
  })
  it('retains an added entry when the runtime adds another setting', () => {
    const changed = setLettaMcpSettings({}, identity, [server])
    const newer = { agents: [{ ...changed.settings.agents[0], nativeField: false }] }
    expect(restoreLettaMcpSettings(newer, identity, changed.previous).agents)
      .toEqual([{ agentId: identity.agentId, baseUrl: identity.baseUrl, nativeField: false }])
  })
  it('rejects duplicate exact scope entries before it writes', () => {
    const entry = { agentId: identity.agentId, baseUrl: identity.baseUrl }
    expect(() => setLettaMcpSettings({ agents: [entry, entry] }, identity, [server])).toThrow('duplicate')
  })
  it.each([null, [], '', { agents: null }, { agents: [null] }])('rejects malformed settings: %j', (value) => {
    expect(() => setLettaMcpSettings(value, identity, [server])).toThrow()
  })
  it('rejects duplicate servers and a relative executable', () => {
    expect(() => setLettaMcpSettings({}, identity, [server, server])).toThrow('unique name')
    expect(() => setLettaMcpSettings({}, identity, [{ ...server, command: 'node' }])).toThrow('absolute stdio command')
  })

  it.each(['null', '{}', '{"name":"echo_probe","transport":"stdio","command":0}'])('rejects malformed server fields before it changes settings: %j', (bytes) => {
    expect(() => setLettaMcpSettings({}, identity, [JSON.parse(bytes)])).toThrow('absolute stdio command')
  })
})

describe('configureLettaMcp', () => {
  function configuration() {
    const home = join(directory, 'home')
    const backendDirectory = join(directory, 'backend')
    mkdirSync(join(home, '.letta'), { recursive: true })
    const path = lettaConversationPath(backendDirectory, identity.conversationId)
    mkdirSync(resolve(path, '..'), { recursive: true })
    writeFileSync(path, JSON.stringify({ id: identity.conversationId, agent_id: identity.agentId }))
    const settings = join(home, '.letta', 'settings.json')
    writeFileSync(settings, JSON.stringify({ nativeExtra: false }))
    return { options: { runDirectory: directory, home, backendDirectory, conversationId: identity.conversationId, servers: [server] }, settings, path }
  }
  it('writes and restores the exact private file while retaining later changes', () => {
    const { options, settings } = configuration()
    const changed = configureLettaMcp(options)
    const current = JSON.parse(readFileSync(settings, 'utf8'))
    expect(current.agents[0].agentId).toBe(identity.agentId)
    expect(current.agents[0].mcpServers[0].args).toEqual(server.args)
    current.laterSetting = 0
    writeFileSync(settings, JSON.stringify(current))
    changed.restore()
    expect(JSON.parse(readFileSync(settings, 'utf8'))).toEqual({ nativeExtra: false, agents: [], laterSetting: 0 })
  })
  it.each(['{', 'null'])('rejects malformed stored conversation bytes without changing settings: %j', (bytes) => {
    const { options, settings, path } = configuration()
    const before = readFileSync(settings, 'utf8')
    writeFileSync(path, bytes)
    expect(() => configureLettaMcp(options)).toThrow()
    expect(readFileSync(settings, 'utf8')).toBe(before)
  })
  it('rejects settings that resolve outside the private HOME', () => {
    const { options, settings } = configuration()
    const outside = join(directory, 'outside-settings.json')
    writeFileSync(outside, '{}')
    rmSync(settings)
    symlinkSync(outside, settings)
    expect(() => configureLettaMcp(options)).toThrow('outside the E2E run')
    expect(readFileSync(outside, 'utf8')).toBe('{}')
  })
})
