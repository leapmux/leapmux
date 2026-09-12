import type { ContentBlock } from '~/lib/contentBlocks'
import { asContentArray } from '~/lib/contentBlocks'
import { pickObject, pickString } from '~/lib/jsonPick'
import { PI_MCP_TOOL } from '../protocol'

/** pi-mcp-adapter supplies identity on proxy and direct tool results. */
export function isPiMcpAdapter(toolName: string, details: Record<string, unknown> | null | undefined): boolean {
  return toolName === PI_MCP_TOOL.Gateway || toolName === PI_MCP_TOOL.Script
    || (!!pickString(details, 'server') && (!!pickString(details, 'tool') || !!pickString(details, 'resourceUri') || !!pickObject(details, 'mcpResult')))
}

/** Rendering and the image viewer must read the same native MCP content. */
export function piNativeMcpContent(toolName: string, result: Record<string, unknown> | null | undefined): ContentBlock[] | null {
  const details = pickObject(result, 'details')
  const native = pickObject(details, 'mcpResult')
  if (!isPiMcpAdapter(toolName, details) || !native || native.omitted === true)
    return null
  const content = asContentArray(native.content)
  if (content)
    return content
  // MCP resource reads return contents; tool calls return content blocks.
  return asContentArray(native.contents)?.map(resource => ({ type: 'resource', resource })) ?? null
}
