import type { McpProbeServer } from '../helpers/mcpProbeServer'
import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import { join } from 'node:path'
import { mcpServersConfig } from '../helpers/mcpProbeServer'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { getGlobalState } from '../helpers/server'
import { loginViaToken, openWorkspace } from '../helpers/ui'
import { withAgentWorkspace } from '../helpers/workspace'
import { DROID_AGENT, nativeContext } from './scenarios'

/**
 * Register `server` in the private Factory home, then run `use` in a new Droid workspace whose agent starts with it.
 * Droid reads its MCP servers from `<home>/.factory/mcp.json` when the agent starts, so the file exists before the
 * workspace opens. The file returns to its exact earlier bytes after the workspace closes.
 */
export async function withDroidMcpWorkspace(
  fixtures: Omit<NativeContextFixtures, 'workspaceId'>,
  options: { server: McpProbeServer, prefix: string },
  use: (context: ManagedNativeScenarioContext) => Promise<void>,
): Promise<void> {
  const home = fixtures.leapmuxServer.agentEnv?.FACTORY_HOME_OVERRIDE
  if (!home)
    throw new Error('The Droid MCP test needs an isolated Factory home.')
  await withNativeConfigurationFile({
    path: join(home, '.factory', 'mcp.json'),
    content: JSON.stringify(mcpServersConfig(options.server)),
    runDir: getGlobalState().tmpDir,
  }, async () => {
    await withAgentWorkspace(fixtures.leapmuxServer, { ...DROID_AGENT, prefix: options.prefix }, async (workspace) => {
      await loginViaToken(fixtures.page, fixtures.leapmuxServer.adminToken)
      await openWorkspace(fixtures.page, workspace.workspaceId)
      await use(await nativeContext({ ...fixtures, workspaceId: workspace.workspaceId }))
    })
  })
}
