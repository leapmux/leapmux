import type { McpProbeServer } from './mcpProbeServer'
import { writeFileSync } from 'node:fs'
import { basename, isAbsolute, join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { assertMcpServerName, mcpProbeServer } from './mcpProbeServer'
import { writeMcpStdioRuntime } from './mcpStdioRuntime'

/** The one tool of a {@link writeMcpNoArgumentToolServer} server, and what it does when an agent calls it. */
export interface McpNoArgumentTool {
  readonly name: string
  /** The description that the tool list states. */
  readonly description: string
  /** The text of the first content item of each result. */
  readonly text: string
  /** The name of a PNG file in the directory of the script. Each result returns it as a second content item. */
  readonly pngFile?: string
  /** The absolute path of a file that the server writes each time the tool runs, before the server replies. */
  readonly calledFile?: string
}

/** The server of a {@link writeMcpNoArgumentToolServer} call. */
export interface McpNoArgumentToolServerOptions {
  /** The name that the server reports in its `serverInfo`. */
  readonly name: string
  /** The base name of the script and of its ready file, such as `image-server`. */
  readonly fileBase: string
  readonly tool: McpNoArgumentTool
}

function scriptSource(stdioPath: string, ready: string, options: McpNoArgumentToolServerOptions): string {
  return `
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { readMcpMessages, sendMcpMessage } from ${JSON.stringify(pathToFileURL(stdioPath).href)};
const directory = dirname(fileURLToPath(import.meta.url));
const tool = ${JSON.stringify(options.tool)};
const listedTool = { name: tool.name, description: tool.description, inputSchema: { type: 'object', properties: {} } };
const emptyArguments = value => typeof value === 'object' && value !== null && !Array.isArray(value) && Object.keys(value).length === 0;
for await (const request of readMcpMessages()) {
  let result;
  switch (request.method) {
    case 'initialize':
      result = { protocolVersion: request.params?.protocolVersion ?? '2025-03-26', capabilities: { tools: {} }, serverInfo: { name: ${JSON.stringify(options.name)}, version: '1' } };
      break;
    case 'tools/list':
      writeFileSync(${JSON.stringify(ready)}, '');
      result = { tools: [listedTool] };
      break;
    case 'tools/call':
      if (request.params?.name !== tool.name || !emptyArguments(request.params?.arguments)) {
        sendMcpMessage({ jsonrpc: '2.0', id: request.id, error: { code: -32602, message: 'The native probe tool requires empty arguments.' } });
        continue;
      }
      if (tool.calledFile !== undefined)
        writeFileSync(tool.calledFile, 'called');
      result = { content: [{ type: 'text', text: tool.text }] };
      if (tool.pngFile !== undefined)
        result.content.push({ type: 'image', mimeType: 'image/png', data: readFileSync(join(directory, tool.pngFile)).toString('base64') });
      break;
    default:
      sendMcpMessage({ jsonrpc: '2.0', id: request.id, error: { code: -32601, message: 'Method not supported' } });
      continue;
  }
  sendMcpMessage({ jsonrpc: '2.0', id: request.id, result });
}
`
}

/**
 * Write a disposable MCP server whose one tool takes no arguments, and keep no receipt of its traffic:
 *
 * - The server reports `options.name` in its `serverInfo`.
 * - The server writes the returned `ready` file when an agent lists its tools, and not before.
 * - A call of another tool, or a call whose arguments are not an empty object, fails with the JSON-RPC error -32602
 *   before the tool has any effect.
 *
 * A server that must prove its traffic keeps a receipt through ./mcpReceiptRuntime.ts, as ./mcpEchoServer.ts does.
 */
export function writeMcpNoArgumentToolServer(directory: string, options: McpNoArgumentToolServerOptions): McpProbeServer & { ready: string } {
  const { fileBase, tool } = options
  // Check every input before the first write, so a refused call leaves no script behind.
  assertMcpServerName(options.name)
  if (fileBase === '' || basename(fileBase) !== fileBase)
    throw new Error(`The MCP server script needs a plain base name, not ${JSON.stringify(fileBase)}.`)
  if (tool.name === '')
    throw new Error('The MCP server tool needs a name.')
  if (tool.pngFile !== undefined && (tool.pngFile === '' || basename(tool.pngFile) !== tool.pngFile))
    throw new Error(`The MCP server image must be a file name in the script directory, not ${JSON.stringify(tool.pngFile)}.`)
  if (tool.calledFile !== undefined && !isAbsolute(tool.calledFile))
    throw new Error(`The MCP server call record needs an absolute path, not ${JSON.stringify(tool.calledFile)}.`)
  const script = join(directory, `${fileBase}.mjs`)
  const ready = join(directory, `${fileBase}-ready`)
  writeFileSync(script, scriptSource(writeMcpStdioRuntime(directory), ready, options))
  return { ...mcpProbeServer(options.name, script), ready }
}
