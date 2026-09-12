import type { McpToolCallSource } from '../../../results/mcpToolCall'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { PI_EVENT } from '~/generated/contracts/pi-protocol'
import { asContentArray } from '~/lib/contentBlocks'
import { prettifyArgsJson, prettifyStructuredJson } from '~/lib/jsonFormat'
import { pickObject, pickString } from '~/lib/jsonPick'
import { parseMcpContentItem } from '../../../results/mcpToolCall'
import { PI_MCP_TOOL } from '../protocol'
import { isPiMcpAdapter, piNativeMcpContent } from './mcp'
import { piExtractTool, piPairedRequest, piPairedResult } from './toolCommon'

/** Pi extensions use the same rich content blocks as the built-in tools. */
export function piGenericToolSource(payload: Record<string, unknown>, request?: ParsedMessageContent, pairedResult?: ParsedMessageContent): McpToolCallSource | null {
  const tool = piExtractTool(payload)
  if (!tool)
    return null
  const result = pickObject(payload, 'result') ?? pickObject(payload, 'partialResult')
  const details = pickObject(result, 'details')
  const args = pickObject(piPairedRequest(payload, request)?.parentObject, 'args') ?? tool.args
  const identity = details ?? pickObject(pickObject(piPairedResult(payload, pairedResult)?.parentObject, 'result'), 'details')
  const native = pickObject(details, 'mcpResult')
  const adapter = isPiMcpAdapter(tool.toolName, identity)
  const nativeContent = piNativeMcpContent(tool.toolName, result)
  let structured: unknown
  if (tool.toolName === PI_MCP_TOOL.Script || !adapter) {
    if (details && Object.keys(details).length > 0)
      structured = details
  }
  else if (native?.omitted !== true) {
    structured = native?.structuredContent
  }
  return {
    server: adapter ? pickString(identity, 'server') : '',
    tool: adapter ? pickString(identity, 'tool') || pickString(args, 'tool') || tool.toolName : tool.toolName,
    argsJson: prettifyArgsJson(tool.toolName === PI_MCP_TOOL.Gateway && typeof args.tool === 'string' ? args.args : args),
    content: (nativeContent ?? asContentArray(result?.content) ?? []).map(parseMcpContentItem),
    structuredJson: structured !== undefined && structured !== null ? prettifyStructuredJson(structured) : undefined,
    status: payload.type === PI_EVENT.ToolExecutionStart || payload.type === PI_EVENT.ToolExecutionUpdate ? 'inProgress' : tool.isError || (adapter && (native?.isError === true || !!pickString(details, 'error'))) ? 'failed' : 'completed',
  }
}
