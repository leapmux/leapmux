import { isAbsolute } from 'node:path'

/*
 * The checks that a private agent environment applies to its inputs before it
 * writes a native configuration. A model endpoint must stay on this machine, and
 * a Model Context Protocol (MCP) server must start from an absolute command, so a
 * fixture cannot send an agent to a real service or to a program that the search
 * path picks.
 */

/** The host names of the loopback interface, as `URL.hostname` spells each one. */
export const LOOPBACK_HOSTNAMES: ReadonlySet<string> = new Set(['127.0.0.1', 'localhost', '[::1]'])

/** One local MCP server that a native configuration starts. */
export interface McpServerLaunch {
  name: string
  command: string
  args: readonly string[]
}

/**
 * Parse `value` as a loopback HTTP URL with no credentials, no query, and no fragment.
 * With `originOnly`, the path must also be `/`, for a client that appends its own path to an origin.
 * A failure states `<label> must be a loopback HTTP URL.`, or `origin` in place of `URL`.
 */
export function requireLoopbackHttpURL(value: string, label: string, options: { originOnly?: boolean } = {}): URL {
  const message = `${label} must be a loopback HTTP ${options.originOnly ? 'origin' : 'URL'}.`
  let endpoint: URL
  try {
    endpoint = new URL(value)
  }
  catch (cause) {
    throw new Error(message, { cause })
  }
  if (endpoint.protocol !== 'http:' || !LOOPBACK_HOSTNAMES.has(endpoint.hostname)
    || endpoint.username || endpoint.password || endpoint.search || endpoint.hash
    || (options.originOnly && endpoint.pathname !== '/')) {
    throw new Error(message)
  }
  return endpoint
}

/**
 * Check each MCP server of a native configuration, and return copies of them in their order.
 *
 * A name must be 1 to `maxNameLength` word characters or hyphens, and the names must be distinct. The native limit
 * of the name differs between agents, so each caller states its own. A command must be absolute.
 */
export function validatedMcpServers(servers: readonly McpServerLaunch[] | undefined, label: string, maxNameLength: number): McpServerLaunch[] {
  if (!Number.isSafeInteger(maxNameLength) || maxNameLength < 1)
    throw new Error(`The ${label} MCP server name limit must be a positive integer.`)
  const pattern = new RegExp(`^[\\w-]{1,${maxNameLength}}$`)
  const names = new Set<string>()
  return (servers ?? []).map((server) => {
    if (!pattern.test(server.name) || names.has(server.name))
      throw new Error(`The ${label} MCP server names must be valid and distinct.`)
    if (!isAbsolute(server.command))
      throw new Error(`The ${label} MCP command must be absolute.`)
    names.add(server.name)
    return { name: server.name, command: server.command, args: [...server.args] }
  })
}
