import type { GenericRequest, GenericResult } from './generic'

/** An MCP call identifies the server and the tool beside the arguments it sent them. */
export interface McpRequest extends GenericRequest { server: string, tool: string }
export type McpResult = GenericResult
