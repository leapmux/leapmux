import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { pathToFileURL } from 'node:url'

import { writeMcpStdioRuntime } from './mcpStdioRuntime'

/** Write a disposable MCP server whose tool records each actual call. */
export function writeMcpPermissionServer(workingDir: string): { script: string, ready: string, called: string, command: string } {
  const script = join(workingDir, 'permission-server.mjs')
  const ready = join(workingDir, 'permission-server-ready')
  const called = join(workingDir, 'permission-server-called')
  writeFileSync(script, `
import { writeFileSync } from 'node:fs';
import { readMcpMessages, sendMcpMessage } from ${JSON.stringify(pathToFileURL(writeMcpStdioRuntime(workingDir)).href)};
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
const directory = dirname(fileURLToPath(import.meta.url));
const send = sendMcpMessage;
for await (const request of readMcpMessages()) {
  let result;
  switch (request.method) {
    case 'initialize':
      result = { protocolVersion: request.params.protocolVersion, capabilities: { tools: {} }, serverInfo: { name: 'permission_probe', version: '1' } };
      break;
    case 'tools/list':
      writeFileSync(join(directory, 'permission-server-ready'), '');
      result = { tools: [{ name: 'touch', description: 'Record a permitted call.', inputSchema: { type: 'object', properties: {} } }] };
      break;
    case 'tools/call':
      if (request.params?.name !== 'touch' || typeof request.params?.arguments !== 'object' || request.params.arguments === null || Array.isArray(request.params.arguments) || Object.keys(request.params.arguments).length !== 0) {
        send({ jsonrpc: '2.0', id: request.id, error: { code: -32602, message: 'The native probe tool requires empty arguments.' } });
        continue;
      }
      writeFileSync(join(directory, 'permission-server-called'), 'called');
      result = { content: [{ type: 'text', text: 'MCP_PERMISSION_TOOL_CALLED' }] };
      break;
    default:
      send({ jsonrpc: '2.0', id: request.id, error: { code: -32601, message: 'Method not supported' } });
      continue;
  }
  send({ jsonrpc: '2.0', id: request.id, result });
}
`)
  return { script, ready, called, command: process.execPath }
}
