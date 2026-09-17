import type { McpCallFacts } from '../../../ir/mcpToolCall'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { PI_EVENT, PI_RESULT_FIELD } from '~/generated/contracts/pi-protocol'
import { asContentArray } from '~/lib/contentBlocks'
import { prettifyArgsJson, prettifyStructuredJson } from '~/lib/jsonFormat'
import { pickObject, pickString } from '~/lib/jsonPick'
import { parseMcpContentItem } from '../../../ir/mcpToolCall'
import { PI_MCP_PROXY_PREFIX, PI_MCP_TOOL } from '../protocol'
import { isPiMcpAdapter, piNativeMcpContent } from './mcp'
import { piExtractTool, piPairedRequest, piPairedResult } from './toolCommon'

/**
 * The MCP server and tool one Pi row states, or undefined for a row that is not an
 * MCP call.
 *
 * pi-mcp-adapter states the pair in the RESULT's `details`. A REQUEST row carries no
 * such record and its tool name is `<server>_<tool>`, which no splitter can divide
 * without the server list -- so the paired result is what supplies it, exactly as it
 * supplies the body below.
 *
 * The namespace proxy is the one name that states its own server: Pi spells it
 * `mcp__<server>` and puts the tool in the arguments, so a request row of that shape
 * answers on its own.
 */
export function piMcpIdentity(
  payload: Record<string, unknown>,
  request?: ParsedMessageContent,
  pairedResult?: ParsedMessageContent,
): { server: string, tool: string } | undefined {
  const tool = piExtractTool(payload)
  if (!tool)
    return undefined
  if (tool.toolName.startsWith(PI_MCP_PROXY_PREFIX)) {
    const server = tool.toolName.slice(PI_MCP_PROXY_PREFIX.length)
    const args = pickObject(piPairedRequest(payload, request)?.parentObject, 'args') ?? tool.args
    if (server)
      return { server, tool: pickString(args, 'tool') || server }
  }
  const identity = piMcpDetails(payload, pairedResult)
  if (!isPiMcpAdapter(tool.toolName, identity))
    return undefined
  const server = pickString(identity, 'server')
  return server ? { server, tool: pickString(identity, 'tool') || tool.toolName } : undefined
}

/** The `details` record that identifies the MCP call: this row's own, else the paired result's. */
function piMcpDetails(payload: Record<string, unknown>, pairedResult?: ParsedMessageContent): Record<string, unknown> | undefined {
  const result = pickObject(payload, 'result') ?? pickObject(payload, 'partialResult')
  return pickObject(result, PI_RESULT_FIELD.Details)
    ?? pickObject(pickObject(piPairedResult(payload, pairedResult)?.parentObject, 'result'), PI_RESULT_FIELD.Details)
    ?? undefined
}

/** Pi extensions use the same rich content blocks as the built-in tools. */
export function piGenericToolSource(payload: Record<string, unknown>, request?: ParsedMessageContent, pairedResult?: ParsedMessageContent): McpCallFacts | null {
  const tool = piExtractTool(payload)
  if (!tool)
    return null
  const result = pickObject(payload, 'result') ?? pickObject(payload, 'partialResult')
  const details = pickObject(result, PI_RESULT_FIELD.Details)
  const args = pickObject(piPairedRequest(payload, request)?.parentObject, 'args') ?? tool.args
  const identity = piMcpDetails(payload, pairedResult)
  const native = pickObject(details, PI_RESULT_FIELD.McpResult)
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
  const structuredJson = structured !== undefined && structured !== null ? prettifyStructuredJson(structured) : undefined
  return {
    server: adapter ? pickString(identity, 'server') : '',
    tool: adapter ? pickString(identity, 'tool') || pickString(args, 'tool') || tool.toolName : tool.toolName,
    argsJson: prettifyArgsJson(tool.toolName === PI_MCP_TOOL.Gateway && typeof args.tool === 'string' ? args.args : args),
    content: (nativeContent ?? asContentArray(result?.content) ?? []).map(parseMcpContentItem),
    ...(structuredJson !== undefined ? { structuredJson } : {}),
    ...(payload.type !== PI_EVENT.ToolExecutionStart && payload.type !== PI_EVENT.ToolExecutionUpdate
      && (tool.isError || (adapter && (native?.isError === true || !!pickString(details, 'error'))))
      ? { failed: true }
      : {}),
  }
}
