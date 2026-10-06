import { existsSync, unlinkSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { DROID_TITLE_RULE, droidTest, expect } from '../droid-fixtures'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { nativeMcpRefusal, readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { droidToolSearchToolCall, mcpToolCall } from '../helpers/providerToolCalls'
import { loginViaToken, messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { withAgentWorkspace } from '../helpers/workspace'
import { nativeDroidCallId } from './toolResult'

droidTest.describe('Factory Droid MCP input form', () => {
  droidTest('shows the native refusal instead of an input form', async ({ page, leapmuxServer, modelScript }) => {
    const home = leapmuxServer.agentEnv.FACTORY_HOME_OVERRIDE
    if (!home)
      throw new Error('the Droid test needs an isolated Factory home')
    const factoryDir = join(home, '.factory')
    const configPath = join(factoryDir, 'mcp.json')
    if (existsSync(configPath))
      throw new Error('the isolated Factory home already has MCP settings')
    const receiptLog = join(factoryDir, 'droid-form-receipt.json')
    const serverPath = writeMcpFormServer(factoryDir, 'droid-form-server.mjs', { receiptLog })
    try {
      writeFileSync(configPath, JSON.stringify({
        mcpServers: { form_probe: { command: process.execPath, args: [serverPath] } },
      }))
      await withAgentWorkspace(leapmuxServer, { provider: AgentProvider.DROID, prefix: 'droid-mcp-form' }, async (workspace) => {
        await loginViaToken(page, leapmuxServer.adminToken)
        await openWorkspace(page, workspace.workspaceId)
        await modelScript.rule(DROID_TITLE_RULE)
        await modelScript.queue(
          { toolCalls: [droidToolSearchToolCall('search-form', 'form_probe ask')] },
          { toolCalls: [mcpToolCall(AgentProvider.DROID, 'call-form', { server: 'form_probe', tool: 'ask', input: {} })] },
          { text: 'The form request failed.' },
        )
        await sendMessage(page, modelScript.prompt('Load form_probe ask, then call it once.'))
        await modelScript.waitForSteps(2)

        const permission = page.locator('[data-testid="control-banner"]:visible')
        await expect(permission).toContainText('form_probe___ask')
        await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

        const status = await modelScript.waitForSteps()
        await waitForAgentIdle(page)
        const finalRequest = status.requests.find(request => request.stepIndex === 2)
        const refusal = nativeMcpRefusal(readMcpServerReceipt(receiptLog))
        expect(refusal.toolResult.id).toBe(refusal.request.toolRequestId)
        const callId = nativeDroidCallId(finalRequest, 'form_probe___ask', 'call-form')
        expect(nativeToolResult(finalRequest, callId)).toContain(refusal.toolResult.text)
        await expect(page.getByTestId('elicitation-form').filter({ visible: true })).toHaveCount(0)
        await expect(permission).toHaveCount(0)
        await expect(messageBubbles(page).filter({ hasText: refusal.toolResult.text }).first()).toBeVisible()
      })
    }
    finally {
      if (existsSync(configPath))
        unlinkSync(configPath)
      unlinkSync(serverPath)
    }
  })
})
