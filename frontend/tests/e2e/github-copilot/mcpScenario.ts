import type { NativeContextFixtures } from '../helpers/nativeScenario'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { COPILOT_PERMISSION_MODE } from '../../../src/generated/contracts/copilot-protocol'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { exerciseMcpProbeFormRoundTrip } from '../helpers/mcpProbeForm'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { getGlobalState } from '../helpers/server'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { COPILOT_AGENT, nativeContext } from './scenarios'

/**
 * Prove the MCP form of Copilot:
 *
 * - Register the probe form server for Copilot.
 * - Open an agent that loads it, and prove that the model offers the tool of the server.
 * - Answer the form through the browser after a reload.
 *
 * Copilot reads `<COPILOT_HOME>/mcp-config.json` when the agent starts, so the file exists before the agent opens.
 * The file returns to its exact earlier bytes after the test.
 */
export async function exerciseCopilotMcpForm(fixtures: NativeContextFixtures): Promise<void> {
  const { leapmuxServer, page, workspaceId } = fixtures
  const home = leapmuxServer.agentEnv?.COPILOT_HOME
  if (!home)
    throw new Error('The Copilot MCP form test needs the isolated Copilot home.')
  const directory = newProviderWorkingDir(COPILOT_AGENT, 'copilot-mcp-form-')
  const server = writeMcpFormServer(directory, 'form-server.mjs')
  const content = JSON.stringify({ mcpServers: { [server.name]: { type: 'local', command: server.command, args: server.args, tools: ['*'] } } })
  await withNativeConfigurationFile({ path: join(home, 'mcp-config.json'), content, runDir: getGlobalState().tmpDir }, async () => {
    await openProviderAgent(leapmuxServer, workspaceId, COPILOT_AGENT, { workingDir: directory, optionValues: { permissionMode: COPILOT_PERMISSION_MODE.AllowAll } })
    await openWorkspace(page, workspaceId)
    const context = await nativeContext(fixtures)
    const catalog = await sendNativeAnswer(context, 'Confirm the MCP server is available.', 'The Copilot MCP server is ready.')
    expect(nativeModelToolNames(catalog)).toContain(mcpToolCall(context.provider, 'catalog', { server: server.name, tool: 'ask', input: {} }).name)
    await exerciseMcpProbeFormRoundTrip(context, { callId: 'copilot-form', reloadBeforeSubmit: true })
  })
}
