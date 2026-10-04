import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { writeMcpReceiptRuntime } from './mcpReceiptRuntime'
import { writeMcpStdioRuntime } from './mcpStdioRuntime'

function serverScript(runtimePath: string, stdioPath: string, receiptLog?: string): string {
  return `
import { readMcpMessages, sendMcpMessage as send } from ${JSON.stringify(pathToFileURL(stdioPath).href)};
import { createMcpServerReceipt } from ${JSON.stringify(pathToFileURL(runtimePath).href)};
const receipt = createMcpServerReceipt(${JSON.stringify(receiptLog ?? null)});
const tools = [{ name: 'echo', description: 'Return the supplied value.', inputSchema: { type: 'object', required: ['value'], properties: { value: { type: 'string' } } } }];
for await (const request of readMcpMessages()) {
  let result;
  switch (request.method) {
    case 'initialize':
      receipt.initialized(request.params?.capabilities ?? {});
      result = { protocolVersion: request.params?.protocolVersion ?? '2025-03-26', capabilities: { tools: {} }, serverInfo: { name: 'echo_probe', version: '1' } };
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
export function writeMcpEchoServer(directory: string, options: { receiptLog?: string } = {}): string {
  const path = join(directory, 'mcp-echo.mjs')
  writeFileSync(path, serverScript(writeMcpReceiptRuntime(directory), writeMcpStdioRuntime(directory), options.receiptLog))
  return path
}
