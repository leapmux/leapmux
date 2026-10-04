import type { LettaMcpCliReceipt } from './mcpCliReceipt'
import { isObject } from '../../../src/lib/jsonPick'

/** Parse only complete CLI stdout after a successful native process exit. */
export function parseLettaMcpCatalog(result: LettaMcpCliReceipt): Record<string, unknown>[] {
  if (result.exitCode !== 0 || result.signal !== null || result.spawnError !== null)
    throw new Error('The native Letta MCP catalog requires a successful CLI receipt.')
  const tools: unknown = JSON.parse(result.stdout)
  if (!Array.isArray(tools) || tools.length === 0 || !tools.every(tool => isObject(tool) && typeof tool.name === 'string' && tool.name !== '' && isObject(tool.inputSchema)))
    throw new Error('The native Letta MCP catalog must contain named tools and their schemas.')
  return tools
}
