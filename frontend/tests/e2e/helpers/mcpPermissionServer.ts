import type { McpProbeServer } from './mcpProbeServer'
import { join } from 'node:path'
import { writeMcpNoArgumentToolServer } from './mcpNoArgumentToolServer'

/** The name that the permission server reports, and the name that each configuration gives it. */
export const MCP_PERMISSION_SERVER_NAME = 'permission_probe'

/**
 * Write a disposable MCP server whose tool records each actual call.
 * The server writes `ready` when an agent lists its tools, and `called` when its tool runs.
 */
export function writeMcpPermissionServer(workingDir: string): McpProbeServer & { ready: string, called: string } {
  const called = join(workingDir, 'permission-server-called')
  const server = writeMcpNoArgumentToolServer(workingDir, {
    name: MCP_PERMISSION_SERVER_NAME,
    fileBase: 'permission-server',
    tool: { name: 'touch', description: 'Record a permitted call.', text: 'MCP_PERMISSION_TOOL_CALLED', calledFile: called },
  })
  return { ...server, called }
}
