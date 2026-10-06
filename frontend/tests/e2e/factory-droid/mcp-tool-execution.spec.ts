import { existsSync, unlinkSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { droidTest, expect } from '../droid-fixtures'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { droidToolSearchToolCall, mcpToolCall } from '../helpers/providerToolCalls'
import { loginViaToken, messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { withAgentWorkspace } from '../helpers/workspace'

droidTest.describe('Factory Droid MCP input form', () => {
  droidTest('executes a disposable MCP tool after native permission approval', async ({ page, leapmuxServer, modelScript }) => {
    const home = leapmuxServer.agentEnv.FACTORY_HOME_OVERRIDE
    if (!home)
      throw new Error('the Droid test needs an isolated Factory home')
    const factoryDir = join(home, '.factory')
    const configPath = join(factoryDir, 'mcp.json')
    if (existsSync(configPath))
      throw new Error('the isolated Factory home already has MCP settings')
    const echoArguments = { query: 'droid', limit: 0, tail: 'END_MCP_ARGUMENTS' }
    const serverPath = writeMcpFormServer(factoryDir, 'droid-echo-server.mjs', { expectedEchoArguments: echoArguments })
    try {
      writeFileSync(configPath, JSON.stringify({
        mcpServers: { form_probe: { command: process.execPath, args: [serverPath] } },
      }))
      await withAgentWorkspace(leapmuxServer, { provider: AgentProvider.DROID, prefix: 'droid-mcp-echo' }, async (workspace) => {
        await loginViaToken(page, leapmuxServer.adminToken)
        await openWorkspace(page, workspace.workspaceId)
        await modelScript.queue(
          { toolCalls: [droidToolSearchToolCall('search-echo', 'form_probe echo')] },
          { toolCalls: [mcpToolCall(AgentProvider.DROID, 'call-echo', { server: 'form_probe', tool: 'echo', input: echoArguments })] },
          { text: 'The MCP echo completed.' },
        )
        await sendMessage(page, modelScript.prompt('Load form_probe echo, then call it once.'))
        await modelScript.waitForSteps(2)
        await expect(page.getByTestId('control-banner').filter({ visible: true })).toContainText('form_probe___echo')
        await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

        const status = await modelScript.waitForSteps()
        await waitForAgentIdle(page)
        expect(JSON.stringify(status.requests.find(request => request.stepIndex === 2)?.body)).toContain('PERMISSION_ACCEPTED')
        await expect(messageBubbles(page).filter({ hasText: 'The MCP echo completed.' }).first()).toBeVisible()
      })
    }
    finally {
      if (existsSync(configPath))
        unlinkSync(configPath)
      unlinkSync(serverPath)
    }
  })
})
