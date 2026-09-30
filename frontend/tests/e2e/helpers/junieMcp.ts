import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

/** Put one local MCP server in the project directory before Junie starts. */
export function writeJunieMcpConfig(workingDir: string, server: string, command: string, args: string[]): void {
  const directory = join(workingDir, '.junie', 'mcp')
  mkdirSync(directory, { recursive: true })
  writeFileSync(join(directory, 'mcp.json'), JSON.stringify({
    mcpServers: { [server]: { command, args } },
  }), { mode: 0o600 })
}
