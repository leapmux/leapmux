import { writeMcpFormServer } from './mcpFormServer'

/** Write a local MCP tool that asks for an empty confirmation form. */
export function writeMcpConfirmationServer(directory: string): string {
  return writeMcpFormServer(directory, 'mcp-confirmation.mjs', { confirmationOnly: true })
}
