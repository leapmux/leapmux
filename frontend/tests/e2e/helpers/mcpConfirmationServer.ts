import { writeFileSync } from 'node:fs'
import { join } from 'node:path'

const script = `
import { createInterface } from 'node:readline';
const send = value => process.stdout.write(JSON.stringify(value) + '\\n');
let toolRequest;
for await (const line of createInterface({ input: process.stdin })) {
  const request = JSON.parse(line);
  if (request.id === undefined) continue;
  if (request.id === 'probe-confirm' && !request.method) {
    const answer = request.result?.action === 'accept' ? 'MCP_CONFIRM_ACCEPTED' : 'MCP_CONFIRM_DECLINED';
    send({ jsonrpc: '2.0', id: toolRequest, result: { content: [{ type: 'text', text: answer }] } });
    continue;
  }
  if (request.method === 'initialize') {
    send({ jsonrpc: '2.0', id: request.id, result: {
      protocolVersion: request.params.protocolVersion,
      capabilities: { tools: {} }, serverInfo: { name: 'form_probe', version: '1' },
    } });
  }
  else if (request.method === 'tools/list') {
    send({ jsonrpc: '2.0', id: request.id, result: {
      tools: [{ name: 'ask', description: 'Ask for an empty confirmation form.', inputSchema: { type: 'object', properties: {} } }],
    } });
  }
  else if (request.method === 'tools/call') {
    toolRequest = request.id;
    send({ jsonrpc: '2.0', id: 'probe-confirm', method: 'elicitation/create', params: {
      mode: 'form', message: 'Allow the probe action?', requestedSchema: { type: 'object', properties: {} },
    } });
  }
  else {
    send({ jsonrpc: '2.0', id: request.id, error: { code: -32601, message: 'Method not supported' } });
  }
}
`

/** Write a local MCP tool that asks MiMo for an empty confirmation form. */
export function writeMcpConfirmationServer(directory: string): string {
  const path = join(directory, 'mcp-confirmation.mjs')
  writeFileSync(path, script)
  return path
}
