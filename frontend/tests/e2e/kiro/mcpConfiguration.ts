import type { McpProbeServer } from '../helpers/mcpProbeServer'
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { mcpServersConfig } from '../helpers/mcpProbeServer'

/**
 * Register `servers` in the project MCP settings of Kiro, `<workingDir>/.kiro/settings/mcp.json`, and return that path.
 * Kiro reads the file when its agent starts in `workingDir`, so call this from the `prepare` step of the agent open.
 */
export function writeKiroProjectMcpServers(workingDir: string, ...servers: McpProbeServer[]): string {
  const settings = join(workingDir, '.kiro', 'settings')
  const path = join(settings, 'mcp.json')
  // Build the content first, so a refused server list writes no directory.
  const content = JSON.stringify(mcpServersConfig(...servers))
  mkdirSync(settings, { recursive: true })
  writeFileSync(path, content)
  return path
}
