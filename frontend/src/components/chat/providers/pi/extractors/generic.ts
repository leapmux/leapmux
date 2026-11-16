import type { McpCallFacts } from '../../../model/mcpToolCall'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { PI_EVENT, PI_TOOL } from '~/generated/contracts/pi-protocol'
import { asContentArray } from '~/lib/contentBlocks'
import { prettifyArgsJson, prettifyJson } from '~/lib/jsonFormat'
import { pickObject } from '~/lib/jsonPick'
import { parseMcpContentItem } from '../../../model/mcpToolCall'
import { PI_MCP_RESOURCE_TOOL, PI_MCP_RESULT_FIELD, PI_TOOL_RESULT_FIELD } from '../protocol'
import { piNativeMcpContent, piNativeMcpIdentity } from './mcp'
import { piExtractTool, piPairedRequest } from './toolCommon'

/**
 * Read native MCP identity from this frame's own result details.
 *
 * A running start frame states no result, so it states no identity: its row keeps the
 * native tool name, and the end frame's row states the server and the tool.
 */
export function piMcpIdentity(payload: Record<string, unknown>): { server: string, tool: string } | undefined {
  const tool = piExtractTool(payload)
  if (!tool)
    return undefined
  const result = pickObject(payload, 'result') ?? pickObject(payload, 'partialResult')
  return piNativeMcpIdentity(tool.toolName, pickObject(result, PI_TOOL_RESULT_FIELD.Details))
}

/** Pi extensions use the same rich content blocks as the built-in tools. */
export function piGenericToolSource(payload: Record<string, unknown>, request?: ParsedMessageContent): McpCallFacts | null {
  const tool = piExtractTool(payload)
  if (!tool)
    return null
  const result = pickObject(payload, 'result') ?? pickObject(payload, 'partialResult')
  const details = pickObject(result, PI_TOOL_RESULT_FIELD.Details)
  const args = pickObject(piPairedRequest(payload, request)?.parentObject, 'args') ?? tool.args
  const identity = piMcpIdentity(payload)
  const native = identity
    ? pickObject(result, PI_MCP_RESULT_FIELD.StructuredContent)
    : undefined
  const nativeContent = piNativeMcpContent(tool.toolName, result)
  let structured: unknown
  if (!identity) {
    if (details && Object.keys(details).length > 0)
      structured = details
  }
  else if (tool.toolName === PI_MCP_RESOURCE_TOOL.List || tool.toolName === PI_MCP_RESOURCE_TOOL.ListTemplates) {
    structured = native
  }
  else if (tool.toolName !== PI_MCP_RESOURCE_TOOL.Read) {
    structured = native?.[PI_MCP_RESULT_FIELD.StructuredContent]
  }
  const structuredJson = structured !== undefined && structured !== null
    ? prettifyJson(JSON.stringify(structured))
    : undefined
  const structuredJsonRole: McpCallFacts['structuredJsonRole'] = structuredJson !== undefined && !identity && tool.toolName === PI_TOOL.Codemode
    ? 'metadata'
    : undefined
  return {
    server: identity?.server ?? '',
    tool: identity?.tool ?? tool.toolName,
    argsJson: prettifyArgsJson(args),
    content: (nativeContent ?? asContentArray(result?.content) ?? []).map(parseMcpContentItem),
    ...(structuredJson !== undefined
      ? { structuredJson }
      : {}),
    ...(structuredJsonRole !== undefined
      ? { structuredJsonRole }
      : {}),
    ...(payload.type !== PI_EVENT.ToolExecutionStart && payload.type !== PI_EVENT.ToolExecutionUpdate
      && (tool.isError || (identity && (result?.isError === true || native?.isError === true)))
      ? { failed: true }
      : {}),
  }
}
