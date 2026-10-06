import type { McpProbeServer } from './mcpProbeServer'
import { writeMcpNoArgumentToolServer } from './mcpNoArgumentToolServer'

/** The name that the image server reports, and the name that each configuration gives it. */
export const MCP_IMAGE_SERVER_NAME = 'image_probe'

/**
 * Write a local Model Context Protocol server that returns a real PNG.
 * `imageName` is the name of the PNG file in `workingDir`. The server reads it when its `show` tool runs.
 * `ready` is the file that the server writes when an agent lists its tools.
 */
export function writeMcpImageServer(workingDir: string, imageName: string): McpProbeServer & { ready: string } {
  return writeMcpNoArgumentToolServer(workingDir, {
    name: MCP_IMAGE_SERVER_NAME,
    fileBase: 'image-server',
    tool: { name: 'show', description: 'Return a local PNG.', text: `MCP image ${imageName}`, pngFile: imageName },
  })
}
