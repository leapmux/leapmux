import type { McpProbeServer } from './mcpProbeServer'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { mcpProbeServer } from './mcpProbeServer'
import { writeMcpReceiptRuntime } from './mcpReceiptRuntime'
import { writeMcpStdioRuntime } from './mcpStdioRuntime'

/** The name that the echo server reports, and the name that each configuration gives it. */
export const MCP_ECHO_SERVER_NAME = 'echo_probe'

function serverScript(runtimePath: string, stdioPath: string, receiptLog?: string): string {
  return `
import { readMcpMessages, sendMcpMessage } from ${JSON.stringify(pathToFileURL(stdioPath).href)};
import { createMcpServerReceipt } from ${JSON.stringify(pathToFileURL(runtimePath).href)};
const receipt = createMcpServerReceipt(${JSON.stringify(receiptLog ?? null)});
const send = message => {
  receipt.sent(message);
  sendMcpMessage(message);
};
const tools = [{ name: 'echo', description: 'Return the supplied value.', inputSchema: { type: 'object', required: ['value'], properties: { value: { type: 'string' } } } }];
for await (const request of readMcpMessages()) {
  receipt.received(request);
  let result;
  switch (request.method) {
    case 'initialize':
      receipt.initialized(request.params?.capabilities ?? {});
      result = { protocolVersion: request.params?.protocolVersion ?? '2025-03-26', capabilities: { tools: {} }, serverInfo: { name: ${JSON.stringify(MCP_ECHO_SERVER_NAME)}, version: '1' } };
      break;
    case 'tools/list':
      receipt.listed(request.id, tools);
      result = { tools };
      break;
    case 'tools/call':
      if (request.params?.name !== 'echo' || typeof request.params?.arguments?.value !== 'string') {
        send({ jsonrpc: '2.0', id: request.id, error: { code: -32602, message: 'An echo call needs a string value.' } });
        continue;
      }
      result = { content: [{ type: 'text', text: 'MCP_ECHO:' + request.params.arguments.value }] };
      receipt.completed(request.id, 'echo', result.content[0].text);
      break;
    default:
      send({ jsonrpc: '2.0', id: request.id, error: { code: -32601, message: 'Method not supported.' } });
      continue;
  }
  send({ jsonrpc: '2.0', id: request.id, result });
}
`
}

/** Write the disposable MCP echo server in the isolated test directory. */
export function writeMcpEchoServer(directory: string, options: { receiptLog?: string } = {}): McpProbeServer {
  const script = join(directory, 'mcp-echo.mjs')
  writeFileSync(script, serverScript(writeMcpReceiptRuntime(directory), writeMcpStdioRuntime(directory), options.receiptLog))
  return mcpProbeServer(MCP_ECHO_SERVER_NAME, script)
}
