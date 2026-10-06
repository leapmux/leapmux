import type { McpProbeServer } from './mcpProbeServer'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { mcpProbeServer } from './mcpProbeServer'
import { writeMcpReceiptRuntime } from './mcpReceiptRuntime'
import { writeMcpStdioRuntime } from './mcpStdioRuntime'

/** The name that the form server reports, and the name that each configuration gives it. */
export const MCP_FORM_SERVER_NAME = 'form_probe'

interface McpFormServerOptions {
  receiptLog?: string
  confirmationOnly?: boolean
  expectedEchoArguments?: { query: string, limit: number, tail: string }
}

function serverScript(options: McpFormServerOptions, runtimePath: string, stdioPath: string): string {
  return `
import { readMcpMessages, sendMcpMessage } from ${JSON.stringify(pathToFileURL(stdioPath).href)};
import { createMcpServerReceipt } from ${JSON.stringify(pathToFileURL(runtimePath).href)};
const receiptLog = ${JSON.stringify(options.receiptLog ?? null)};
const expectedEchoArguments = ${JSON.stringify(options.expectedEchoArguments ?? null)};
const receipt = createMcpServerReceipt(receiptLog);
const send = message => {
  receipt.sent(message);
  sendMcpMessage(message);
};
const confirmationOnly = ${JSON.stringify(options.confirmationOnly ?? false)};
const pending = new Map();
let sequence = 0;
const object = value => typeof value === 'object' && value !== null && !Array.isArray(value);
const toolResult = (id, tool, text, isError = false) => {
  receipt.completed(id, tool, text, isError);
  send({ jsonrpc: '2.0', id, result: { content: [{ type: 'text', text }], ...(isError ? { isError: true } : {}) } });
};
for await (const request of readMcpMessages()) {
  receipt.received(request);
  if (request.method === undefined) {
    const tool = pending.get(request.id);
    if (!tool) continue;
    let text;
    let isError = false;
    if (object(request.error) && Number.isSafeInteger(request.error.code) && typeof request.error.message === 'string') {
      const error = { code: request.error.code, message: request.error.message, ...('data' in request.error ? { data: request.error.data } : {}) };
      receipt.replied({ id: request.id, kind: 'error', error });
      text = 'FORM_ROUND_TRIP_REFUSED: ' + error.code + ' ' + error.message;
      isError = true;
    }
    else if (object(request.result)) {
      const reply = request.result;
      receipt.replied({ id: request.id, kind: 'result', result: reply });
      const valid = reply.action === 'accept' && object(reply.content)
        && reply.content.count === 0 && reply.content.enabled === false && reply.content.color === 'b';
      text = confirmationOnly ? (reply.action === 'accept' ? 'MCP_CONFIRM_ACCEPTED' : reply.action === 'decline' || reply.action === 'cancel' ? 'MCP_CONFIRM_DECLINED' : 'FORM_ROUND_TRIP_FAILED') : valid ? 'FORM_ROUND_TRIP_OK'
        : reply.action === 'decline' ? 'FORM_ROUND_TRIP_DECLINED'
          : reply.action === 'cancel' ? 'FORM_ROUND_TRIP_CANCELLED' : 'FORM_ROUND_TRIP_FAILED';
      isError = text === 'FORM_ROUND_TRIP_FAILED';
    }
    else { text = 'FORM_ROUND_TRIP_FAILED'; isError = true; }
    pending.delete(request.id);
    toolResult(tool.id, tool.name, text, isError);
    continue;
  }
  const params = object(request.params) ? request.params : {};
  switch (request.method) {
    case 'initialize': {
      receipt.initialized(object(params.capabilities) ? params.capabilities : {});
      send({ jsonrpc: '2.0', id: request.id, result: {
        protocolVersion: typeof params.protocolVersion === 'string' ? params.protocolVersion : '2025-03-26',
        capabilities: { tools: {} }, serverInfo: { name: ${JSON.stringify(MCP_FORM_SERVER_NAME)}, version: '1' },
      } });
      break;
    }
    case 'tools/list': {
      const tools = [
        { name: 'ask', description: 'Request the disposable probe form. Call once with no arguments.', inputSchema: { type: 'object', properties: {}, additionalProperties: false } },
        { name: 'echo', description: 'Echo approved arguments.', inputSchema: { type: 'object', properties: { query: { type: 'string' }, limit: { type: 'integer' }, tail: { type: 'string' } }, required: ['query', 'limit', 'tail'], additionalProperties: false } },
      ];
      receipt.listed(request.id, tools);
      send({ jsonrpc: '2.0', id: request.id, result: { tools } });
      break;
    }
    case 'tools/call': {
      if (params.name === 'echo') {
        const args = params.arguments;
        const valid = expectedEchoArguments !== null && object(args) && Object.keys(args).length === 3
          && args.query === expectedEchoArguments.query && args.limit === expectedEchoArguments.limit && args.tail === expectedEchoArguments.tail;
        toolResult(request.id, 'echo', valid ? 'PERMISSION_ACCEPTED' : 'PERMISSION_ARGUMENTS_FAILED', !valid);
        break;
      }
      if (params.name !== 'ask' || params.arguments !== undefined && (!object(params.arguments) || Object.keys(params.arguments).length !== 0)) {
        send({ jsonrpc: '2.0', id: request.id, error: { code: -32602, message: 'The probe tool or its arguments are invalid.' } });
        break;
      }
      const prefix = confirmationOnly ? 'probe-confirm' : 'probe-form';
      const id = sequence++ === 0 ? prefix : prefix + '-' + sequence;
      const elicitation = confirmationOnly ? { mode: 'form', message: 'Allow the probe action?', requestedSchema: { type: 'object', properties: {} } } : { mode: 'form', message: 'Choose the probe settings.', requestedSchema: { type: 'object', required: ['count', 'enabled', 'color'], properties: {
        count: { type: 'integer', title: 'Count', minimum: 0, maximum: 3 },
        enabled: { type: 'boolean', title: 'Enabled' },
        color: { type: 'string', title: 'Color', oneOf: [{ const: 'b', title: 'Blue' }, { const: 'r', title: 'Red' }] },
      } } };
      pending.set(id, { id: request.id, name: 'ask' });
      receipt.requested(id, request.id, elicitation);
      send({ jsonrpc: '2.0', id, method: 'elicitation/create', params: elicitation });
      break;
    }
    case 'ping':
      send({ jsonrpc: '2.0', id: request.id, result: {} });
      break;
    default:
      send({ jsonrpc: '2.0', id: request.id, error: { code: -32601, message: 'Method not supported.' } });
  }
}
`
}

/** Write a disposable MCP server that asks for the probe form. */
export function writeMcpFormServer(directory: string, filename: string, options: McpFormServerOptions = {}): McpProbeServer {
  const script = join(directory, filename)
  writeFileSync(script, serverScript(options, writeMcpReceiptRuntime(directory), writeMcpStdioRuntime(directory)))
  return mcpProbeServer(MCP_FORM_SERVER_NAME, script)
}
