import type { McpProbeServer } from './mcpProbeServer'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { mcpProbeServer } from './mcpProbeServer'
import { writeMcpStdioRuntime } from './mcpStdioRuntime'

/** The name that the permission server reports, and the name that each configuration gives it. */
export const MCP_PERMISSION_SERVER_NAME = 'permission_probe'

/**
 * Write a disposable MCP server whose tool records each actual call.
 * The server writes `ready` when an agent lists its tools, and `called` when its tool runs.
 */
export function writeMcpPermissionServer(workingDir: string): McpProbeServer & { ready: string, called: string } {
  const script = join(workingDir, 'permission-server.mjs')
  const ready = join(workingDir, 'permission-server-ready')
  const called = join(workingDir, 'permission-server-called')
  writeFileSync(script, `
import { writeFileSync } from 'node:fs';
import { readMcpMessages, sendMcpMessage } from ${JSON.stringify(pathToFileURL(writeMcpStdioRuntime(workingDir)).href)};
const send = sendMcpMessage;
for await (const request of readMcpMessages()) {
  let result;
  switch (request.method) {
    case 'initialize':
      result = { protocolVersion: request.params.protocolVersion, capabilities: { tools: {} }, serverInfo: { name: ${JSON.stringify(MCP_PERMISSION_SERVER_NAME)}, version: '1' } };
      break;
    case 'tools/list':
      writeFileSync(${JSON.stringify(ready)}, '');
      result = { tools: [{ name: 'touch', description: 'Record a permitted call.', inputSchema: { type: 'object', properties: {} } }] };
      break;
    case 'tools/call':
      if (request.params?.name !== 'touch' || typeof request.params?.arguments !== 'object' || request.params.arguments === null || Array.isArray(request.params.arguments) || Object.keys(request.params.arguments).length !== 0) {
        send({ jsonrpc: '2.0', id: request.id, error: { code: -32602, message: 'The native probe tool requires empty arguments.' } });
        continue;
      }
      writeFileSync(${JSON.stringify(called)}, 'called');
      result = { content: [{ type: 'text', text: 'MCP_PERMISSION_TOOL_CALLED' }] };
      break;
    default:
      send({ jsonrpc: '2.0', id: request.id, error: { code: -32601, message: 'Method not supported' } });
      continue;
  }
  send({ jsonrpc: '2.0', id: request.id, result });
}
`)
  return { ...mcpProbeServer(MCP_PERMISSION_SERVER_NAME, script), ready, called }
}
