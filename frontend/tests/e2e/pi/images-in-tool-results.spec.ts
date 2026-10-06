import type { Page } from '@playwright/test'
import type { ModelScript } from '../helpers/modelScriptFixture'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { openAgentViaAPI } from '../helpers/api'
import { writeMcpImageServer } from '../helpers/mcpImageServer'
import { writeMcpResultServer } from '../helpers/mcpResultServer'
import { mcpToolCall, piMcpResourceToolCall, readToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { withMockPiModel } from '../helpers/scriptedPiModel'
import { getGlobalState } from '../helpers/server'
import { expectDecodedImageInBubble, expectMcpToolImage, expectToolRowImage, writeToolImage } from '../helpers/toolImages'
import { openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { piTest } from '../pi-fixtures'
import { writePiMcpConfiguration } from './mcpConfiguration'
import { readPiMcpResult } from './mcpResult'

async function proveToolImage(page: Page, modelScript: ModelScript, provider: AgentProvider, workingDir: string, approveRead = false): Promise<void> {
  const fileName = writeToolImage(workingDir, String(provider))
  await modelScript.queue(
    { toolCalls: [readToolCall(provider, 'read-image-probe', join(workingDir, fileName))] },
    { text: `I inspected ${fileName}.` },
  )
  await sendMessage(page, modelScript.prompt(`Read ${fileName} and describe it.`))
  if (approveRead) {
    await modelScript.waitForSteps(1)
    const permission = page.getByTestId('control-banner').filter({ visible: true })
    await expect(permission).toContainText(fileName)
    await page.getByTestId('control-actions').getByRole('button', { name: 'Allow', exact: true }).click()
  }
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  await expectToolRowImage(page, fileName)
}

piTest('shows the picture returned by Read', async ({ authenticatedPiWorkspace, page, modelScript }) => {
  const workingDir = authenticatedPiWorkspace.workingDir
  if (!workingDir)
    throw new Error('Pi test workspace has no working directory')
  await proveToolImage(page, modelScript, AgentProvider.PI, workingDir)
})

piTest('shows one decoded native MCP image and resource image after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const directory = createTestDirectory('pi-native-mcp-images-')
  const imageName = writeToolImage(directory, 'native-pi-mcp')
  const image = writeMcpImageServer(directory, imageName)
  const resource = writeMcpResultServer(directory, { receiptLog: join(directory, 'resource-receipt.json'), imagePath: join(directory, imageName) })
  writePiMcpConfiguration(directory, getGlobalState().tmpDir, { image_probe: { command: image.command, args: image.args }, result_probe: { command: process.execPath, args: [resource] } })
  await withMockPiModel(directory, leapmuxServer, async (settings) => {
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, directory, { agentProvider: AgentProvider.PI, ...settings })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const calls = [
      mcpToolCall(AgentProvider.PI, 'native-pi-mcp-image', { server: 'image_probe', tool: 'show', input: {} }),
      piMcpResourceToolCall('native-pi-resource-image', { operation: 'read', server: 'result_probe', uri: 'probe://image' }),
    ]
    const nativeContext = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.PI }
    const imageData = readFileSync(join(directory, imageName)).toString('base64')
    for (const call of calls) {
      const start = (await modelScript.status()).stepCount
      await modelScript.queue({ toolCalls: [call] }, { text: `The native image ${call.id} reached the model.` })
      await sendMessage(page, modelScript.prompt(`Read the native image ${call.id}.`))
      await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      const native = await readPiMcpResult(nativeContext, call.id, call.name)
      expect(native.failed).toBe(false)
      if (call.id === 'native-pi-mcp-image') {
        expect(native.result.structuredContent).toEqual({ content: [{ type: 'text', text: `MCP image ${imageName}` }, { type: 'image', mimeType: 'image/png', data: imageData }] })
        await expectMcpToolImage(page, imageName, call.id)
      }
      else {
        expect(native.result.structuredContent).toEqual({ server: 'result_probe', uri: 'probe://image', contents: [{ uri: 'probe://image', mimeType: 'image/png', blob: imageData }] })
        const bubble = page.locator('[data-testid="message-bubble"][data-tool-call-id="native-pi-resource-image"][data-tool-row-role="result"]:visible')
        await expect(bubble).toHaveCount(1)
        await expectDecodedImageInBubble(bubble)
      }
    }
    await expect(page.locator('[data-chat-scroll-container="true"]:visible button[aria-label="Open image"]:visible img:visible')).toHaveCount(2)
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expect(page.locator('[data-chat-scroll-container="true"]:visible button[aria-label="Open image"]:visible img:visible')).toHaveCount(2)
    await expectMcpToolImage(page, imageName, 'native-pi-mcp-image')
    await expectDecodedImageInBubble(page.locator('[data-testid="message-bubble"][data-tool-call-id="native-pi-resource-image"][data-tool-row-role="result"]:visible'))
  })
})
