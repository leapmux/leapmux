import type { McpProbeServer } from './mcpProbeServer'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { mcpProbeServer } from './mcpProbeServer'
import { writeMcpReceiptRuntime } from './mcpReceiptRuntime'
import { writeMcpStdioRuntime } from './mcpStdioRuntime'

export const MCP_SAMPLING_SERVER_NAME = 'sampling_probe'

/** Write a local tool that asks its host for one model response. */
export function writeMcpSamplingServer(directory: string, options: { prompt?: string, receiptLog?: string } = {}): McpProbeServer {
  const prompt = options.prompt ?? 'Reply with the single word SAMPLED.'
  if (!prompt.trim())
    throw new Error('The sampling probe needs a nonempty prompt.')
  const stdio = pathToFileURL(writeMcpStdioRuntime(directory)).href
  const receipts = pathToFileURL(writeMcpReceiptRuntime(directory)).href
  const script = join(directory, 'mcp-sampling-server.mjs')
  writeFileSync(script, `
import { readMcpMessages, sendMcpMessage } from ${JSON.stringify(stdio)};
import { createMcpServerReceipt } from ${JSON.stringify(receipts)};
const receipt = createMcpServerReceipt(${JSON.stringify(options.receiptLog ?? null)});
const pending = new Map();
let sequence = 0;
const object = value => typeof value === 'object' && value !== null && !Array.isArray(value);
const send = message => { receipt.sent(message); sendMcpMessage(message); };
const complete = (id, text, isError) => {
  receipt.completed(id, 'sample', text, isError);
  send({ jsonrpc: '2.0', id, result: { content: [{ type: 'text', text }], ...(isError ? { isError: true } : {}) } });
};
for await (const request of readMcpMessages()) {
  receipt.received(request);
  if (request.method === undefined) {
    if (!pending.has(request.id)) continue;
    const id = pending.get(request.id);
    pending.delete(request.id);
    if (object(request.error)) {
      complete(id, 'MCP_SAMPLING_REFUSED: ' + String(request.error.message ?? 'The host refused sampling.'), true);
      continue;
    }
    const content = object(request.result) ? request.result.content : undefined;
    const blocks = Array.isArray(content) ? content : [content];
    const text = blocks.filter(object).filter(block => block.type === 'text' && typeof block.text === 'string').map(block => block.text).join('\\n');
    if (object(request.result) && request.result.stopReason === 'cancelled') {
      complete(id, 'MCP_SAMPLING_REFUSED: ' + (text || 'The host cancelled sampling.'), true);
      continue;
    }
    complete(id, text || 'MCP_SAMPLING_FAILED: The response contains no text.', !text);
    continue;
  }
  const params = object(request.params) ? request.params : {};
  switch (request.method) {
    case 'initialize':
      receipt.initialized(object(params.capabilities) ? params.capabilities : {});
      send({ jsonrpc: '2.0', id: request.id, result: {
        protocolVersion: typeof params.protocolVersion === 'string' ? params.protocolVersion : '2025-03-26',
        capabilities: { tools: {} }, serverInfo: { name: ${JSON.stringify(MCP_SAMPLING_SERVER_NAME)}, version: '1' },
      } });
      break;
    case 'tools/list': {
      const tools = [{ name: 'sample', description: 'Ask the host model for the local sampling probe.', inputSchema: { type: 'object', properties: {}, additionalProperties: false } }];
      receipt.listed(request.id, tools);
      send({ jsonrpc: '2.0', id: request.id, result: { tools } });
      break;
    }
    case 'tools/call': {
      if (params.name !== 'sample' || params.arguments !== undefined && (!object(params.arguments) || Object.keys(params.arguments).length !== 0)) {
        send({ jsonrpc: '2.0', id: request.id, error: { code: -32602, message: 'The sampling tool or its arguments are invalid.' } });
        break;
      }
      const id = 'sampling-' + ++sequence;
      pending.set(id, request.id);
      send({ jsonrpc: '2.0', id, method: 'sampling/createMessage', params: {
        messages: [{ role: 'user', content: { type: 'text', text: ${JSON.stringify(prompt)} } }],
        maxTokens: 32,
      } });
      break;
    }
    case 'ping':
      send({ jsonrpc: '2.0', id: request.id, result: {} });
      break;
    default:
      send({ jsonrpc: '2.0', id: request.id, error: { code: -32601, message: 'Method not supported.' } });
  }
}
`)
  return mcpProbeServer(MCP_SAMPLING_SERVER_NAME, script)
}
