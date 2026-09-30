import { writeFileSync } from 'node:fs'
import { join } from 'node:path'

const serverScript = `
import { createInterface } from 'node:readline';
const send = value => process.stdout.write(JSON.stringify(value) + '\\n');
for await (const line of createInterface({ input: process.stdin })) {
  const request = JSON.parse(line);
  if (request.id === undefined) continue;
  let result;
  switch (request.method) {
    case 'initialize':
      result = { protocolVersion: request.params.protocolVersion, capabilities: { tools: {} }, serverInfo: { name: 'echo_probe', version: '1' } };
      break;
    case 'tools/list':
      result = { tools: [{ name: 'echo', description: 'Return the supplied value.', inputSchema: { type: 'object', required: ['value'], properties: { value: { type: 'string' } } } }] };
      break;
    case 'tools/call':
      if (request.params?.name !== 'echo' || typeof request.params?.arguments?.value !== 'string') {
        send({ jsonrpc: '2.0', id: request.id, error: { code: -32602, message: 'An echo call needs a string value.' } });
        continue;
      }
      result = { content: [{ type: 'text', text: 'MCP_ECHO:' + request.params.arguments.value }] };
      break;
    default:
      send({ jsonrpc: '2.0', id: request.id, error: { code: -32601, message: 'Method not supported' } });
      continue;
  }
  send({ jsonrpc: '2.0', id: request.id, result });
}
`

/** Write the disposable MCP echo server in the isolated test directory. */
export function writeMcpEchoServer(directory: string): string {
  const path = join(directory, 'mcp-echo.mjs')
  writeFileSync(path, serverScript)
  return path
}
