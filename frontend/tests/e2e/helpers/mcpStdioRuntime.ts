import { writeFileSync } from 'node:fs'
import { join } from 'node:path'

const runtime = `
import { createInterface } from 'node:readline';
export const sendMcpMessage = value => process.stdout.write(JSON.stringify(value) + '\\n');
export async function* readMcpMessages() {
  for await (const line of createInterface({ input: process.stdin })) {
    let request;
    try { request = JSON.parse(line); }
    catch { sendMcpMessage({ jsonrpc: '2.0', id: null, error: { code: -32700, message: 'Invalid JSON.' } }); continue; }
    if (typeof request !== 'object' || request === null || Array.isArray(request) || request.id === undefined) continue;
    if (typeof request.id !== 'string' && !(typeof request.id === 'number' && Number.isSafeInteger(request.id))) {
      sendMcpMessage({ jsonrpc: '2.0', id: null, error: { code: -32600, message: 'Invalid request ID.' } });
      continue;
    }
    const hasMethod = Object.hasOwn(request, 'method');
    const hasResult = Object.hasOwn(request, 'result');
    const hasError = Object.hasOwn(request, 'error');
    const validRequest = hasMethod && typeof request.method === 'string' && request.method.length > 0 && !hasResult && !hasError;
    const error = request.error;
    const validError = typeof error === 'object' && error !== null && !Array.isArray(error) && Number.isSafeInteger(error.code) && typeof error.message === 'string';
    const validReply = !hasMethod && hasResult !== hasError && (!hasError || validError);
    if (request.jsonrpc !== '2.0' || (!validRequest && !validReply)) {
      sendMcpMessage({ jsonrpc: '2.0', id: request.id, error: { code: -32600, message: 'Invalid JSON-RPC envelope.' } });
      continue;
    }
    yield request;
  }
}
`

/** Write the shared stdio transport beside the private MCP scenario server. */
export function writeMcpStdioRuntime(directory: string): string {
  const path = join(directory, 'mcp-stdio-runtime.mjs')
  writeFileSync(path, runtime)
  return path
}
