import type { McpProbeServer } from '../helpers/mcpProbeServer'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { GEMINI_AGENT } from '../gemini-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { getGlobalState } from '../helpers/server'
import { tabById } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { closeAgentViaAPI } from '../helpers/worktree'

/** Reload the actual MCP settings in a new native agent and restore their exact bytes. */
export async function withGeminiMcp(context: ManagedNativeScenarioContext, server: McpProbeServer, use: () => Promise<void>): Promise<void> {
  const home = context.leapmuxServer.agentEnv?.GEMINI_CLI_HOME
  if (!home)
    throw new Error('The Gemini MCP scenario requires the private native home.')
  const path = join(home, '.gemini/settings.json')
  const settings: unknown = JSON.parse(readFileSync(path, 'utf8'))
  if (!isObject(settings))
    throw new Error('The native Gemini settings must be an object.')
  const existing = isObject(settings.mcpServers) ? settings.mcpServers : {}
  const content = JSON.stringify({ ...settings, mcpServers: { ...existing, [server.name]: { command: server.command, args: server.args } } })
  const keeper = await currentNativeAgent(context)
  await withNativeConfigurationFile({ path, content, runDir: getGlobalState().tmpDir }, async () => {
    const opened = await openProviderAgent(context.leapmuxServer, context.workspaceId, GEMINI_AGENT, { optionValues: { permissionMode: 'yolo' } })
    await withCleanup(async () => {
      await tabById(context.page, opened.agentId).click()
      await currentNativeAgent(context)
      await use()
    }, async () => {
      const close = await closeAgentViaAPI(context.leapmuxServer.hubUrl, context.leapmuxServer.adminToken, context.leapmuxServer.workerId, opened.agentId)
      expect(close.failureMessage).toBe('')
      await tabById(context.page, keeper.id).click()
      await currentNativeAgent(context)
    })
  })
}
