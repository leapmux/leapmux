import { existsSync, statSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'

import { writeMcpReceiptRuntime } from './mcpReceiptRuntime'
import { writeMcpStdioRuntime } from './mcpStdioRuntime'

export type McpResultContent = { type: 'text', text: string } | { type: 'image', path: string }

/** Write a neutral MCP server for complete results, failures, and resources. */
export function writeMcpResultServer(directory: string, options: { receiptLog: string, imagePath?: string, inspectContent?: readonly McpResultContent[], inspectNullable?: null }): string {
  if (options.inspectNullable !== undefined && options.inspectNullable !== null)
    throw new Error('The MCP inspect nullable field requires an explicit null value.')
  if (options.inspectContent !== undefined) {
    if (!Array.isArray(options.inspectContent))
      throw new Error('The MCP inspect fixture requires a native content array.')
    for (const block of options.inspectContent) {
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
  const script = join(directory, 'mcp-results.mjs')
  writeFileSync(script, `
import { readFileSync } from 'node:fs';
import { writeMcpReceiptValue } from ${JSON.stringify(pathToFileURL(writeMcpReceiptRuntime(directory)).href)};
import { readMcpMessages, sendMcpMessage } from ${JSON.stringify(pathToFileURL(writeMcpStdioRuntime(directory)).href)};
const receiptLog = ${JSON.stringify(options.receiptLog)};
const imagePath = ${JSON.stringify(options.imagePath ?? null)};
const inspectContent = ${JSON.stringify(options.inspectContent ?? null)};
const includeNullable = ${options.inspectNullable === null};
const receipts = [];
const object = value => typeof value === 'object' && value !== null && !Array.isArray(value);
const send = reply => {
  receipts.push({ reply });
  writeMcpReceiptValue(receiptLog, receipts);
  sendMcpMessage(reply);
};
const tools = [
  { name: 'inspect', description: 'Return complete arguments and computed structured values.', inputSchema: { type: 'object', properties: { count: { type: 'integer' }, enabled: { type: 'boolean' }, text: { type: 'string' } }, required: ['count', 'enabled', 'text'], additionalProperties: false } },
  { name: 'fail', description: 'Return a failed MCP result.', inputSchema: { type: 'object', properties: {}, additionalProperties: false } },
];
for await (const request of readMcpMessages()) {
  receipts.push({ request });
  const params = object(request.params) ? request.params : {};
  let result;
  switch (request.method) {
    case 'initialize':
      result = { protocolVersion: params.protocolVersion, capabilities: { tools: {}, resources: {} }, serverInfo: { name: 'result_probe', version: '1' } };
      break;
    case 'tools/list': result = { tools }; break;
    case 'tools/call': {
      const args = params.arguments;
      if (params.name === 'fail' && object(args) && Object.keys(args).length === 0) {
        result = { content: [{ type: 'text', text: 'NATIVE_MCP_FAILED_RESULT' }], structuredContent: { failed: true, count: 0 }, isError: true };
        break;
      }
      if (params.name !== 'inspect' || !object(args) || Object.keys(args).length !== 3 || !Number.isSafeInteger(args.count) || typeof args.enabled !== 'boolean' || typeof args.text !== 'string') {
        send({ jsonrpc: '2.0', id: request.id, error: { code: -32602, message: 'Invalid native MCP arguments.' } });
        continue;
      }
      result = { content: inspectContent === null ? [{ type: 'text', text: 'NATIVE_MCP_INSPECT:' + JSON.stringify(args) }] : inspectContent.map(block => block.type === 'text' ? block : { type: 'image', mimeType: 'image/png', data: readFileSync(block.path).toString('base64') }), structuredContent: { nextCount: args.count + 1, enabled: args.enabled, text: args.text, ...(includeNullable ? { nullable: null } : {}) }, _meta: { privateFixture: true } };
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
  return script
}
