import { mkdirSync, writeFileSync } from 'node:fs'
import { isAbsolute, join } from 'node:path'
import { assertPiConfigurationPath } from './privateConfigurationPath'

interface PiMcpServer {
  command: string
  args: readonly string[]
}

/** Write native project MCP configuration after validating each private path. */
export function writePiMcpConfiguration(directory: string, runDir: string, servers: Record<string, PiMcpServer>): string {
  assertPiConfigurationPath(directory, runDir)
  const entries = Object.entries(servers)
  if (entries.length === 0)
    throw new Error('The native Pi MCP fixture requires a server.')
  for (const [name, server] of entries) {
    if (!/^[\w-]+$/.test(name) || !isAbsolute(server.command) || server.args.some(value => typeof value !== 'string'))
      throw new Error('The native Pi MCP fixture requires a valid server name and absolute command.')
  }
  const project = join(directory, '.pi')
  assertPiConfigurationPath(project, runDir)
  mkdirSync(project, { recursive: true })
  const path = join(project, 'mcp.json')
  assertPiConfigurationPath(path, runDir)
  writeFileSync(path, JSON.stringify({ mcpServers: Object.fromEntries(entries.map(([name, server]) => [name, { command: server.command, args: [...server.args], exposure: 'direct' }])) }), { mode: 0o600 })
  return path
}
