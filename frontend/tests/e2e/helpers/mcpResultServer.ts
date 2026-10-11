import type { McpProbeServer } from './mcpProbeServer'
import { existsSync, statSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { mcpProbeServer } from './mcpProbeServer'
import { writeMcpReceiptRuntime } from './mcpReceiptRuntime'
import { writeMcpStdioRuntime } from './mcpStdioRuntime'

/** The name that the result server reports, and the name that each configuration gives it. */
export const MCP_RESULT_SERVER_NAME = 'result_probe'

export type McpResultContent = { type: 'text', text: string } | { type: 'image', path: string }

/** What the result server returns. */
export interface McpResultServerOptions {
  /** The receipt that the server writes. `./mcpServerReceipt.ts` reads it. */
  receiptLog: string
  /** An image that the `probe://image` resource returns. Without it, the server lists no image resource. */
  imagePath?: string
  /** The content blocks that `inspect` returns, in order. The default is one text block that states the arguments. */
  inspectContent?: readonly McpResultContent[]
  /** Add an explicit `nullable: null` field to the structured result of `inspect`. */
  includeNullable?: boolean
}

/** Refuse a content list that the server could not return as stated. */
function validateInspectContent(content: readonly McpResultContent[]): void {
  if (!Array.isArray(content))
    throw new Error('The MCP inspect fixture requires a native content array.')
  for (const block of content) {
    if (!block || typeof block !== 'object')
      throw new Error('The MCP inspect fixture contains an invalid native block.')
    if (block.type === 'text') {
      if (typeof block.text !== 'string')
        throw new Error('The MCP inspect text block requires its exact text value.')
    }
    else if (block.type !== 'image' || typeof block.path !== 'string' || !existsSync(block.path) || !statSync(block.path).isFile()) {
      throw new Error('The MCP inspect fixture requires existing image paths.')
    }
  }
}

/** Write a neutral MCP server for complete results, failures, and resources. */
export function writeMcpResultServer(directory: string, options: McpResultServerOptions): McpProbeServer {
  if (options.inspectContent !== undefined)
    validateInspectContent(options.inspectContent)
  const script = join(directory, 'mcp-results.mjs')
  writeFileSync(script, `
import { readFileSync } from 'node:fs';
import { createMcpServerReceipt } from ${JSON.stringify(pathToFileURL(writeMcpReceiptRuntime(directory)).href)};
import { readMcpMessages, sendMcpMessage } from ${JSON.stringify(pathToFileURL(writeMcpStdioRuntime(directory)).href)};
const receipt = createMcpServerReceipt(${JSON.stringify(options.receiptLog)});
const imagePath = ${JSON.stringify(options.imagePath ?? null)};
const inspectContent = ${JSON.stringify(options.inspectContent ?? null)};
const includeNullable = ${JSON.stringify(options.includeNullable ?? false)};
const object = value => typeof value === 'object' && value !== null && !Array.isArray(value);
const send = message => {
  receipt.sent(message);
  sendMcpMessage(message);
};
const textOf = content => content.filter(block => block.type === 'text').map(block => block.text).join('\\n');
const tools = [
  { name: 'inspect', description: 'Return complete arguments and computed structured values.', inputSchema: { type: 'object', properties: { count: { type: 'integer' }, enabled: { type: 'boolean' }, text: { type: 'string' } }, required: ['count', 'enabled', 'text'], additionalProperties: false } },
  { name: 'fail', description: 'Return a failed MCP result.', inputSchema: { type: 'object', properties: {}, additionalProperties: false } },
];
for await (const request of readMcpMessages()) {
  receipt.received(request);
  const params = object(request.params) ? request.params : {};
  let result;
  switch (request.method) {
    case 'initialize':
      receipt.initialized(object(params.capabilities) ? params.capabilities : {});
      result = { protocolVersion: params.protocolVersion, capabilities: { tools: {}, resources: {} }, serverInfo: { name: ${JSON.stringify(MCP_RESULT_SERVER_NAME)}, version: '1' } };
      break;
    case 'tools/list':
      receipt.listed(request.id, tools);
      result = { tools };
      break;
    case 'tools/call': {
      const args = params.arguments;
      if (params.name === 'fail' && object(args) && Object.keys(args).length === 0) {
        result = { content: [{ type: 'text', text: 'NATIVE_MCP_FAILED_RESULT' }], structuredContent: { failed: true, count: 0 }, isError: true };
        receipt.completed(request.id, 'fail', textOf(result.content), true);
        break;
      }
      if (params.name !== 'inspect' || !object(args) || Object.keys(args).length !== 3 || !Number.isSafeInteger(args.count) || typeof args.enabled !== 'boolean' || typeof args.text !== 'string') {
        send({ jsonrpc: '2.0', id: request.id, error: { code: -32602, message: 'Invalid native MCP arguments.' } });
        continue;
      }
      result = { content: inspectContent === null ? [{ type: 'text', text: 'NATIVE_MCP_INSPECT:' + JSON.stringify(args) }] : inspectContent.map(block => block.type === 'text' ? block : { type: 'image', mimeType: 'image/png', data: readFileSync(block.path).toString('base64') }), structuredContent: { nextCount: args.count + 1, enabled: args.enabled, text: args.text, ...(includeNullable ? { nullable: null } : {}) }, _meta: { privateFixture: true } };
      receipt.completed(request.id, 'inspect', textOf(result.content), false);
      break;
    }
    case 'resources/list':
      result = { resources: [{ uri: 'probe://text', name: 'Native text', mimeType: 'text/plain' }, ...(imagePath ? [{ uri: 'probe://image', name: 'Native image', mimeType: 'image/png' }] : [])] };
      break;
    case 'resources/templates/list':
      result = { resourceTemplates: [{ uriTemplate: 'probe://template/{id}', name: 'Native template', mimeType: 'text/plain' }] };
      break;
    case 'resources/read':
      if (params.uri === 'probe://text') result = { contents: [{ uri: params.uri, mimeType: 'text/plain', text: 'NATIVE_MCP_RESOURCE_TEXT' }] };
      else if (params.uri === 'probe://image' && imagePath) result = { contents: [{ uri: params.uri, mimeType: 'image/png', blob: readFileSync(imagePath).toString('base64') }] };
      else { send({ jsonrpc: '2.0', id: request.id, error: { code: -32602, message: 'Unknown native MCP resource.' } }); continue; }
      break;
    default:
      send({ jsonrpc: '2.0', id: request.id, error: { code: -32601, message: 'Method not supported.' } });
      continue;
  }
  send({ jsonrpc: '2.0', id: request.id, result });
}
`)
  return mcpProbeServer(MCP_RESULT_SERVER_NAME, script)
}
