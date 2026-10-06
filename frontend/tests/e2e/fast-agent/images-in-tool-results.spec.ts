import { existsSync } from 'node:fs'
import { expect, FAST_AGENT_AGENT, fastAgentTest } from '../fastagent-fixtures'
import { writeMcpImageServer } from '../helpers/mcpImageServer'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { expectMcpToolImage, writeToolImage } from '../helpers/toolImages'
import { answerControl, openWorkspace, sendMessage, toolRows, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { nativeContext } from './scenarios'

fastAgentTest.describe('Fast Agent images in tool results', () => {
  fastAgentTest('renders the image returned by a local MCP tool', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    const { workingDir } = await openProviderAgent(leapmuxServer, context.workspaceId, FAST_AGENT_AGENT)
    const imageName = writeToolImage(workingDir, 'fastagent-mcp')
    const server = writeMcpImageServer(workingDir, imageName)
    await openWorkspace(page, context.workspaceId)

    await sendMessage(page, `/mcp connect --name image_probe ${JSON.stringify(server.command)} ${JSON.stringify(server.script)}`)
    await waitForAgentIdle(page)
    await expect.poll(() => existsSync(server.ready)).toBe(true)

    const callID = 'show-fastagent-image'
    const start = await modelScript.queue(
      { toolCalls: [mcpToolCall(context.provider, callID, { server: 'image_probe', tool: 'show', input: {} })] },
      { text: 'The MCP tool returned an image.' },
    )
    await sendMessage(page, modelScript.prompt('Call image_probe show.'))
    await modelScript.waitForSteps(start + 1)
    await expect(await waitForControlBanner(page)).toContainText('show')
    const nativeRequest = toolRows(page).filter({ hasText: 'show' }).last()
    await expect(nativeRequest).toBeVisible()
    const acpCallID = await nativeRequest.locator('xpath=ancestor::*[@data-testid="message-bubble"][1]').getAttribute('data-tool-call-id')
    if (!acpCallID)
      throw new Error('the Fast Agent MCP call has no native ACP ID')
    expect(acpCallID).not.toBe(callID)
    await answerControl(page, 'allow')
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)

    expect(JSON.stringify((await modelScript.requestAt(start + 1)).body)).toContain(`MCP image ${imageName}`)
    await expectMcpToolImage(page, imageName, acpCallID)
  })
})
