import process from 'node:process'

/**
 * One disposable MCP server that a test writes, and how an agent starts it.
 *
 * Every `writeMcp*Server` writer returns this shape. `name` is the name that the server reports in its
 * `serverInfo`, and the name that a native configuration gives the server, so a tool call reaches the server under
 * the name that the server states.
 */
export interface McpProbeServer {
  readonly name: string
  /** The absolute path of the server script. */
  readonly script: string
  /** The executable that starts the server: the Node.js runtime of the test process. */
  readonly command: string
  readonly args: readonly string[]
}

/** An MCP server name that a native configuration accepts as a key and as part of a tool name. */
const MCP_SERVER_NAME = /^[\w-]+$/

/**
 * Refuse a name that a native configuration cannot hold as a key, or that a tool name cannot hold.
 * `McpProbeServer` is a structural type, so each configuration builder checks the name of the server that it gets.
 */
export function assertMcpServerName(name: string): void {
  if (!MCP_SERVER_NAME.test(name))
    throw new Error(`An MCP server name holds only letters, digits, "_", and "-", not ${JSON.stringify(name)}.`)
}

/** Describe the launch of one server script through the Node.js runtime of the test process. */
export function mcpProbeServer(name: string, script: string): McpProbeServer {
  assertMcpServerName(name)
  if (script.trim() === '')
    throw new Error('An MCP server needs the path of its script.')
  return { name, script, command: process.execPath, args: [script] }
}

/** The common native MCP configuration: one `{ command, args }` entry for each server, under its name. */
export interface McpServersConfig {
  mcpServers: Record<string, { command: string, args: string[] }>
}

/**
 * Build the `mcpServers` map that most native configurations read.
 * A provider whose configuration has another shape builds its entry at its own site from the server's `name`,
 * `command`, and `args`.
 */
export function mcpServersConfig(...servers: readonly McpProbeServer[]): McpServersConfig {
  if (servers.length === 0)
    throw new Error('An MCP configuration needs at least one server.')
  const mcpServers: McpServersConfig['mcpServers'] = {}
  for (const server of servers) {
    assertMcpServerName(server.name)
    if (Object.hasOwn(mcpServers, server.name))
      throw new Error(`The MCP configuration holds two servers named ${server.name}.`)
    mcpServers[server.name] = { command: server.command, args: [...server.args] }
  }
  return { mcpServers }
}
