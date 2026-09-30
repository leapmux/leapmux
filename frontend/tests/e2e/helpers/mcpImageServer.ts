import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'

/** Write a local Model Context Protocol server that returns a real PNG. */
export function writeMcpImageServer(workingDir: string, imageName: string): { script: string, ready: string, command: string, args: string[] } {
  const script = join(workingDir, 'image-server.mjs')
  const ready = join(workingDir, 'image-server-ready')
  writeFileSync(script, `
import { readFileSync, writeFileSync } from 'node:fs';
import { createInterface } from 'node:readline';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
const directory = dirname(fileURLToPath(import.meta.url));
const send = value => process.stdout.write(JSON.stringify(value) + '\\n');
for await (const line of createInterface({ input: process.stdin })) {
  const request = JSON.parse(line);
  if (request.id === undefined) continue;
  let result;
  switch (request.method) {
    case 'initialize':
      result = { protocolVersion: request.params.protocolVersion, capabilities: { tools: {} }, serverInfo: { name: 'image_probe', version: '1' } };
      break;
    case 'tools/list':
      writeFileSync(join(directory, 'image-server-ready'), '');
      result = { tools: [{ name: 'show', description: 'Return a local PNG.', inputSchema: { type: 'object', properties: {} } }] };
      break;
    case 'tools/call':
      result = { content: [
        { type: 'text', text: ${JSON.stringify(`MCP image ${imageName}`)} },
        { type: 'image', mimeType: 'image/png', data: readFileSync(join(directory, ${JSON.stringify(imageName)})).toString('base64') },
      ] };
      break;
    default:
      send({ jsonrpc: '2.0', id: request.id, error: { code: -32601, message: 'Method not supported' } });
      continue;
  }
  send({ jsonrpc: '2.0', id: request.id, result });
}
`)
  return { script, ready, command: process.execPath, args: [script] }
}
