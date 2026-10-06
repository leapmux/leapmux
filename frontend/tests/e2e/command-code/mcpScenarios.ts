import type { McpProbeServer } from '../helpers/mcpProbeServer'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { COMMAND_CODE_AGENT } from '../command-code-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { getGlobalState } from '../helpers/server'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { closeAgentViaAPI } from '../helpers/worktree'

/**
 * Run `run` in a real Command Code session that loads one native MCP server:
 *
 * - Install the server before the session starts.
 * - Open the session, and run `run`.
 * - Close the session, then return the configuration to its exact earlier bytes.
 *
 * Command Code reads its MCP servers from `~/.commandcode/mcp.json`, which the isolated profile already holds.
 */
export async function withCommandCodeMcp(context: ManagedNativeScenarioContext, options: { server: McpProbeServer, workingDir: string }, run: () => Promise<void>): Promise<void> {
  const home = context.leapmuxServer.agentEnv?.HOME
  if (!home)
    throw new Error('The native Command Code MCP proof requires a private home.')
  const runDir = getGlobalState().tmpDir
  const path = join(home, '.commandcode/mcp.json')
  // Check the configuration before the read, so a path that leaves the run is never read.
  assertPrivateNativePath(path, runDir)
  assertPrivateNativePath(options.server.script, runDir)
  // The profile must hold the configuration already: the read fails for a missing file.
  const config: unknown = JSON.parse(readFileSync(path, 'utf8'))
  if (!isObject(config) || !isObject(config.mcpServers))
    throw new Error('The private native MCP config has an invalid server table.')
  const content = JSON.stringify({ ...config, mcpServers: { ...config.mcpServers, [options.server.name]: { command: options.server.command, args: options.server.args } } })
  await withNativeConfigurationFile({ path, content, runDir }, async () => {
    let agentID: string | undefined
    await withCleanup(async () => {
      const agent = await openProviderAgent(context.leapmuxServer, context.workspaceId, COMMAND_CODE_AGENT, { optionValues: { permissionMode: 'bypass' }, workingDir: options.workingDir })
      agentID = agent.agentId
      await openWorkspace(context.page, context.workspaceId)
      await run()
    }, async () => {
      if (!agentID)
        return
      const close = await closeAgentViaAPI(context.leapmuxServer.hubUrl, context.leapmuxServer.adminToken, context.leapmuxServer.workerId, agentID)
      expect(close.failureMessage).toBe('')
    })
  })
}
