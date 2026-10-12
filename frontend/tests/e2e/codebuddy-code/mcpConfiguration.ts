import type { McpProbeServer } from '../helpers/mcpProbeServer'
import { join } from 'node:path'
import { mcpServersConfig } from '../helpers/mcpProbeServer'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { getGlobalState } from '../helpers/server'

/**
 * Install `server` as the user MCP server of the isolated CodeBuddy profile while `use` runs, and restore the exact
 * profile file after success or failure. CodeBuddy reads its user servers from `.mcp.json` in its config directory.
 */
export async function withCodebuddyUserMcpServer(environment: Readonly<Record<string, string | undefined>>, server: McpProbeServer, use: () => Promise<void>): Promise<void> {
  const configDir = environment.CODEBUDDY_CONFIG_DIR
  if (!configDir)
    throw new Error('The CodeBuddy end-to-end environment requires an isolated config directory.')
  await withNativeConfigurationFile({ path: join(configDir, '.mcp.json'), content: JSON.stringify(mcpServersConfig(server)), runDir: getGlobalState().tmpDir }, use)
}
