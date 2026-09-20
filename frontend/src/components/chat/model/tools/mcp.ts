import type { GenericToolRequest, GenericToolResult } from './generic'

/** An MCP call identifies the server and the tool beside the arguments it sent them. */
export interface McpRequest extends GenericToolRequest { server: string, tool: string }
export type McpResult = GenericToolResult
