import { Buffer } from 'node:buffer'
import { randomUUID } from 'node:crypto'
import { existsSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs'
import { isAbsolute, join, resolve } from 'node:path'
import { isObject } from '../../../src/lib/jsonPick'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'

export interface LettaConversationIdentity {
  conversationId: string
  agentId: string
  baseUrl: string
}

export interface LettaMcpServer {
  name: string
  transport: 'stdio'
  command: string
  args: string[]
}

export type PreviousLettaMcpSettings
  = { kind: 'entry-absent' }
    | { kind: 'field-absent' }
    | { kind: 'field-present', value: unknown }

function sessionId(value: string): void {
  if (!value.trim() || value !== value.trim() || value === 'default')
    throw new Error('The native Letta conversation ID must identify a nondefault stored conversation.')
}

export function lettaConversationPath(backendDirectory: string, conversationId: string): string {
  sessionId(conversationId)
  if (!isAbsolute(backendDirectory))
    throw new Error('The native Letta backend directory must be absolute.')
  const key = Buffer.from(`conversation:${conversationId}`, 'utf8').toString('base64url')
  return join(backendDirectory, 'conversations', key, 'conversation.json')
}

/** Read the native identity without substituting the Worker agent ID. */
export function parseLettaConversation(value: unknown, conversationId: string, backendDirectory: string): LettaConversationIdentity {
  sessionId(conversationId)
  if (!isAbsolute(backendDirectory))
    throw new Error('The native Letta backend directory must be absolute.')
  if (!isObject(value) || value.id !== conversationId)
    throw new Error('The stored native Letta conversation ID does not match the requested session.')
  if (typeof value.agent_id !== 'string' || value.agent_id.trim() === '' || value.agent_id !== value.agent_id.trim())
    throw new Error('The stored native Letta conversation must identify its agent.')
  return { conversationId, agentId: value.agent_id, baseUrl: `local:${resolve(backendDirectory)}` }
}

function settingsAgents(value: unknown): { settings: Record<string, unknown>, agents: Record<string, unknown>[] } {
  if (!isObject(value))
    throw new Error('The native Letta settings must be an object.')
  const agents = value.agents === undefined ? [] : value.agents
  if (!Array.isArray(agents) || !agents.every(isObject))
    throw new Error('The native Letta agents settings must be an array of objects.')
  return { settings: value, agents }
}

function matchingEntry(agents: Record<string, unknown>[], identity: LettaConversationIdentity): number {
  if (!identity.agentId.trim() || !identity.baseUrl.startsWith('local:') || !isAbsolute(identity.baseUrl.slice('local:'.length)))
    throw new Error('The native Letta MCP settings require an agent ID and an absolute local backend.')
  const matches = agents.flatMap((entry, index) => entry.agentId === identity.agentId && entry.baseUrl === identity.baseUrl ? [index] : [])
  if (matches.length > 1)
    throw new Error('The native Letta MCP settings contain duplicate agent and backend entries.')
  return matches[0] ?? -1
}

function validateServers(servers: readonly LettaMcpServer[]): void {
  const seen = new Set<string>()
  for (const server of servers) {
    if (!isObject(server) || typeof server.name !== 'string' || !/^[\w-]+$/.test(server.name) || seen.has(server.name)
      || server.transport !== 'stdio' || typeof server.command !== 'string' || !isAbsolute(server.command)
      || !Array.isArray(server.args) || !server.args.every(argument => typeof argument === 'string')) {
      throw new Error('The native Letta MCP server requires a unique name and an absolute stdio command.')
    }
    seen.add(server.name)
  }
}

/** Change only the MCP field of the exact native agent and backend entry. */
export function setLettaMcpSettings(value: unknown, identity: LettaConversationIdentity, servers: readonly LettaMcpServer[]) {
  validateServers(servers)
  const { settings, agents } = settingsAgents(value)
  const index = matchingEntry(agents, identity)
  const entry: Record<string, unknown> | undefined = index < 0 ? { agentId: identity.agentId, baseUrl: identity.baseUrl } : agents[index]
  if (!entry)
    throw new Error('The matched native Letta MCP entry disappeared before its update.')
  const previous: PreviousLettaMcpSettings = index < 0
    ? { kind: 'entry-absent' }
    : Object.hasOwn(entry, 'mcpServers') ? { kind: 'field-present', value: entry.mcpServers } : { kind: 'field-absent' }
  const next = { ...entry, mcpServers: servers.map(server => ({ ...server, args: [...server.args] })) }
  return { settings: { ...settings, agents: index < 0 ? [...agents, next] : agents.map((entry, entryIndex) => entryIndex === index ? next : entry) }, previous }
}

/** Restore the MCP field and retain newer native settings. */
export function restoreLettaMcpSettings(value: unknown, identity: LettaConversationIdentity, previous: PreviousLettaMcpSettings) {
  const { settings, agents } = settingsAgents(value)
  const index = matchingEntry(agents, identity)
  if (index < 0)
    throw new Error('The configured native Letta agent entry disappeared before restoration.')
  const entry = { ...agents[index] }
  if (previous.kind === 'field-present')
    entry.mcpServers = previous.value
  else
    delete entry.mcpServers
  const removeEntry = previous.kind === 'entry-absent' && Object.keys(entry).every(key => key === 'agentId' || key === 'baseUrl')
  return { ...settings, agents: agents.flatMap((current, entryIndex) => entryIndex !== index ? [current] : removeEntry ? [] : [entry]) }
}

function writeSettings(path: string, value: Record<string, unknown>): void {
  const partial = `${path}.${randomUUID()}.partial`
  try {
    writeFileSync(partial, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 })
    renameSync(partial, path)
  }
  finally {
    rmSync(partial, { force: true })
  }
}

/** Register private native servers and retain a restoration operation for that field. */
export function configureLettaMcp(options: {
  runDirectory: string
  home: string
  backendDirectory: string
  conversationId: string
  servers: readonly LettaMcpServer[]
}) {
  for (const directory of [options.home, options.backendDirectory])
    assertPrivateNativePath(directory, options.runDirectory)
  const conversation = lettaConversationPath(options.backendDirectory, options.conversationId)
  assertPrivateNativePath(conversation, options.runDirectory)
  const identity = parseLettaConversation(JSON.parse(readFileSync(conversation, 'utf8')), options.conversationId, options.backendDirectory)
  const path = join(options.home, '.letta', 'settings.json')
  assertPrivateNativePath(path, options.home)
  const current = JSON.parse(readFileSync(path, 'utf8'))
  const changed = setLettaMcpSettings(current, identity, options.servers)
  writeSettings(path, changed.settings)
  return {
    identity,
    restore: () => {
      if (!existsSync(path))
        throw new Error('The private native Letta settings disappeared before restoration.')
      assertPrivateNativePath(path, options.home)
      writeSettings(path, restoreLettaMcpSettings(JSON.parse(readFileSync(path, 'utf8')), identity, changed.previous))
    },
  }
}
