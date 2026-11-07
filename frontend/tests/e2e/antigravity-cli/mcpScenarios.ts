import type { McpProbeServer } from '../helpers/mcpProbeServer'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { join } from 'node:path'
import { withCleanup } from '../helpers/cleanup'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { getGlobalState } from '../helpers/server'
import { tabById } from '../helpers/ui'
import { closeNativeAgentAndWait } from '../helpers/workerTabs'
import { openProviderAgent } from '../helpers/workspace'
import { ANTIGRAVITY_AGENT } from './scenarios'

/** Reload the actual MCP settings in a new native agent and restore their exact bytes. */
export async function withAntigravityMcp(context: ManagedNativeScenarioContext, server: McpProbeServer, use: () => Promise<void>): Promise<void> {
  const home = context.leapmuxServer.agentEnv?.HOME
  if (!home)
    throw new Error('The Antigravity MCP scenario requires the private native home.')
  const path = join(home, '.gemini', 'config', 'mcp_config.json')
  const content = JSON.stringify({
    mcpServers: {
      [server.name]: { command: server.command, args: server.args },
    },
  })
  const keeper = await currentNativeAgent(context)
  await withNativeConfigurationFile({ path, content, runDir: getGlobalState().tmpDir }, async () => {
    const opened = await openProviderAgent(context.leapmuxServer, context.workspaceId, ANTIGRAVITY_AGENT, {
      optionValues: { permissionMode: 'bypass' },
    })
    await withCleanup(async () => {
      await tabById(context.page, opened.agentId).click()
      await currentNativeAgent(context)
      await sendNativeAnswer(context, 'Initialize the native MCP connection.', 'The native MCP connection is ready.')
      await use()
    }, async () => {
      await closeNativeAgentAndWait(context, opened.agentId)
      await tabById(context.page, keeper.id).click()
      await currentNativeAgent(context)
    })
  })
}
