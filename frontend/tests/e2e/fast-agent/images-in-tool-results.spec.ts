import { existsSync } from 'node:fs'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, fastAgentTest, openFastAgentAgent } from '../fastagent-fixtures'
import { writeMcpImageServer } from '../helpers/mcpImageServer'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { expectMcpToolImage, writeToolImage } from '../helpers/toolImages'
import { openWorkspace, sendMessage, toolRows, waitForAgentIdle } from '../helpers/ui'

fastAgentTest.describe('Fast Agent images in tool results', () => {
  fastAgentTest('renders the image returned by a local MCP tool', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openFastAgentAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    const imageName = writeToolImage(workingDir, 'fastagent-mcp')
    const server = writeMcpImageServer(workingDir, imageName)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    await sendMessage(page, `/mcp connect --name image_probe ${JSON.stringify(server.command)} ${JSON.stringify(server.script)}`)
    await waitForAgentIdle(page)
    await expect.poll(() => existsSync(server.ready)).toBe(true)

    const callID = 'show-fastagent-image'
    await modelScript.queue(
      { toolCalls: [mcpToolCall(AgentProvider.FAST_AGENT, callID, { server: 'image_probe', tool: 'show', input: {} })] },
      { text: 'The MCP tool returned an image.' },
    )
    await sendMessage(page, modelScript.prompt('Call image_probe show.'))
    await modelScript.waitForSteps(1)
    const banner = page.locator('[data-testid="control-banner"]:visible')
    await expect(banner).toContainText('show')
    const nativeRequest = toolRows(page).filter({ hasText: 'show' }).last()
    await expect(nativeRequest).toBeVisible()
    const acpCallID = await nativeRequest.locator('xpath=ancestor::*[@data-testid="message-bubble"][1]').getAttribute('data-tool-call-id')
    if (!acpCallID)
      throw new Error('the Fast Agent MCP call has no native ACP ID')
    expect(acpCallID).not.toBe(callID)
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    expect(JSON.stringify(status.requests.find(request => request.stepIndex === 1)?.body)).toContain(`MCP image ${imageName}`)
    await expectMcpToolImage(page, imageName, acpCallID)
  })
})
