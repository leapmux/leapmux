import type { ContentBlock } from '~/lib/contentBlocks'
import { asContentArray } from '~/lib/contentBlocks'
import { pickObject, pickString } from '~/lib/jsonPick'
import { PI_MCP_RESOURCE_TOOL, PI_MCP_RESULT_FIELD, PI_MCP_TOOL_PREFIX, PI_TOOL_RESULT_FIELD } from '../protocol'

/** Read the original native identity. Sanitized tool names cannot supply it. */
export function piNativeMcpIdentity(toolName: string, details: Record<string, unknown> | null | undefined): { server: string, tool: string } | undefined {
  const server = pickString(details, PI_MCP_RESULT_FIELD.Server)
  const tool = pickString(details, PI_MCP_RESULT_FIELD.Tool)
  if (toolName.startsWith(PI_MCP_TOOL_PREFIX))
    return server && tool ? { server, tool } : undefined
  if (tool !== toolName)
    return undefined
  if (toolName === PI_MCP_RESOURCE_TOOL.Read)
    return server ? { server, tool } : undefined
  // A listing without a server covers every connected server.
  if ((toolName === PI_MCP_RESOURCE_TOOL.List || toolName === PI_MCP_RESOURCE_TOOL.ListTemplates)
    && typeof details?.[PI_MCP_RESULT_FIELD.Server] === 'string') {
    return { server, tool }
  }
  return undefined
}

/** Rendering and the image viewer must read the same native MCP content. */
export function piNativeMcpContent(toolName: string, result: Record<string, unknown> | null | undefined): ContentBlock[] | null {
  const details = pickObject(result, PI_TOOL_RESULT_FIELD.Details)
  const native = pickObject(result, PI_MCP_RESULT_FIELD.StructuredContent)
  if (!piNativeMcpIdentity(toolName, details) || !native)
    return null
  // Resource reads keep their original URIs and blobs beside the model's converted content.
  if (toolName === PI_MCP_RESOURCE_TOOL.Read)
    return asContentArray(native[PI_MCP_RESULT_FIELD.Contents])?.map(resource => ({ type: 'resource', resource })) ?? null
  return toolName.startsWith(PI_MCP_TOOL_PREFIX) ? asContentArray(native[PI_TOOL_RESULT_FIELD.Content]) : null
}
