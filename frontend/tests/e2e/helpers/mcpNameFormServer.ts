import type { McpProbeServer } from './mcpProbeServer'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { mcpProbeServer } from './mcpProbeServer'
import { writeMcpStdioRuntime } from './mcpStdioRuntime'

/** What {@link writeMcpNameFormServer} writes. */
export interface McpNameFormServerOptions {
  /** The name that the server reports, and the name that the native configuration gives it. */
  serverName: string
  /** The name that the reader types into the form. The tool result states whether the reply holds it. */
  expectedName: string
}

/**
 * Write a private MCP server whose one tool, `ask`, asks for a form with one text field, `name`.
 *
 * The server writes `ready` when an agent lists its tools, so a test can prove the discovery. The tool result is
 * `FORM_ROUND_TRIP_OK` when the reply accepts the form and holds `expectedName`, and `FORM_ROUND_TRIP_FAILED`
 * otherwise.
 */
export function writeMcpNameFormServer(directory: string, options: McpNameFormServerOptions): McpProbeServer & { ready: string } {
  if (options.expectedName === '')
    throw new Error('The name form needs a name that the reader types.')
  const script = join(directory, 'mcp-name-form.mjs')
  const ready = join(directory, 'mcp-name-form-ready')
  const server = mcpProbeServer(options.serverName, script)
  writeFileSync(script, `
import { writeFileSync } from 'node:fs';
import { readMcpMessages, sendMcpMessage as send } from ${JSON.stringify(pathToFileURL(writeMcpStdioRuntime(directory)).href)};
const ready = ${JSON.stringify(ready)};
const expectedName = ${JSON.stringify(options.expectedName)};
const formRequestId = 'probe-form';
let toolRequest;
for await (const request of readMcpMessages()) {
  if (request.id === formRequestId && request.method === undefined) {
    const reply = request.result;
    const valid = reply?.action === 'accept' && reply.content?.name === expectedName;
    send({ jsonrpc: '2.0', id: toolRequest, result: { content: [{ type: 'text', text: valid ? 'FORM_ROUND_TRIP_OK' : 'FORM_ROUND_TRIP_FAILED' }] } });
    continue;
  }
  let result;
  switch (request.method) {
    case 'initialize':
      result = { protocolVersion: request.params.protocolVersion, capabilities: { tools: {} }, serverInfo: { name: ${JSON.stringify(server.name)}, version: '1' } };
      break;
    case 'tools/list':
      writeFileSync(ready, '');
      result = { tools: [{ name: 'ask', description: 'Request the disposable probe form. Call once with no arguments.', inputSchema: { type: 'object', properties: {} } }] };
      break;
    case 'tools/call':
      toolRequest = request.id;
      send({ jsonrpc: '2.0', id: formRequestId, method: 'elicitation/create', params: { mode: 'form', message: 'Name the probe.', requestedSchema: { type: 'object', required: ['name'], properties: { name: { type: 'string', title: 'Name' } } } } });
      continue;
    default:
      send({ jsonrpc: '2.0', id: request.id, error: { code: -32601, message: 'Method not supported' } });
      continue;
  }
  send({ jsonrpc: '2.0', id: request.id, result });
}
`)
  return { ...server, ready }
}
