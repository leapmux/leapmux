import type { McpProbeServer } from './mcpProbeServer'
import { writeMcpFormServer } from './mcpFormServer'

/** Write a local MCP tool that asks for an empty confirmation form. The server reports the form server's name. */
export function writeMcpConfirmationServer(directory: string): McpProbeServer {
  return writeMcpFormServer(directory, 'mcp-confirmation.mjs', { confirmationOnly: true })
}
