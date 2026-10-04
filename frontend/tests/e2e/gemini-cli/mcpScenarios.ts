import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { isObject } from '../../../src/lib/jsonPick'
import { openGeminiAgent } from '../gemini-fixtures'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { getGlobalState } from '../helpers/server'
import { sendMessage, tabById } from '../helpers/ui'
import { closeAgentViaAPI } from '../helpers/worktree'

/** Reload the actual MCP settings in a new native agent and restore their exact bytes. */
export async function withGeminiMcp(context: ManagedNativeScenarioContext, server: { name: string, command: string, args: string[] }, use: () => Promise<void>): Promise<void> {
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
    const opened = await openGeminiAgent(context.leapmuxServer, context.workspaceId, { permissionMode: 'yolo' })
    await tabById(context.page, opened.agentId).click()
    try {
      await currentNativeAgent(context)
      await use()
    }
    finally {
      await closeAgentViaAPI(context.leapmuxServer.hubUrl, context.leapmuxServer.adminToken, context.leapmuxServer.workerId, opened.agentId)
      await tabById(context.page, keeper.id).click()
      await currentNativeAgent(context)
    }
  })
}

/** Return the exact native model request that carries a real MCP result. */
export async function invokeGeminiMcp(context: ManagedNativeScenarioContext, options: { server: string, tool: string, callId: string, input: Record<string, unknown> }): Promise<MockModelRequestRecord> {
  const start = (await context.modelScript.status()).stepCount
  await context.modelScript.queue({ toolCalls: [mcpToolCall(context.provider, options.callId, options)] }, { text: 'The native MCP operation completed.' })
  await sendMessage(context.page, context.modelScript.prompt('Run the scripted native MCP operation.'))
  await waitForNativeToolSteps(context, start + 2)
  const request = (await context.modelScript.status()).requests.find(row => row.stepIndex === start + 1)
  if (!request)
    throw new Error('The native MCP result reached no following model request.')
  return request
}
