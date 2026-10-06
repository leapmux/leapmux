import type { McpProbeServer } from '../helpers/mcpProbeServer'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { isObject } from '../../../src/lib/jsonPick'
import { assertMcpServerName } from '../helpers/mcpProbeServer'

/** The settings file of Amp in the private config home of `agentEnv`. */
export function ampSettingsPath(agentEnv: Readonly<Record<string, string>> | undefined): string {
  const configHome = agentEnv?.XDG_CONFIG_HOME
  if (!configHome)
    throw new Error('The isolated Amp config home is unavailable.')
  return join(configHome, 'amp', 'settings.json')
}

/**
 * Read the Amp settings at `path`, and return them with `server` as the only MCP server.
 * The other settings stay. An absent file reads as no settings. The MCP servers of the file do not stay, so the agent
 * starts the server of the test alone.
 */
export function ampMcpSettings(path: string, server: McpProbeServer): Record<string, unknown> {
  assertMcpServerName(server.name)
  const settings: unknown = existsSync(path) ? JSON.parse(readFileSync(path, 'utf8')) : {}
  if (!isObject(settings))
    throw new Error('The private Amp settings must hold a JSON object.')
  return { ...settings, 'amp.mcpServers': { [server.name]: { command: server.command, args: [...server.args] } } }
}
