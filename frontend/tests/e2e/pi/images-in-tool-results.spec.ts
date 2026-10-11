import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { openAgentViaAPI } from '../helpers/api'
import { writeMcpImageServer } from '../helpers/mcpImageServer'
import { writeMcpResultServer } from '../helpers/mcpResultServer'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { mcpToolCall, piMcpResourceToolCall, readToolCall } from '../helpers/providerToolCalls'
import { newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { getGlobalState } from '../helpers/server'
import { expectDecodedImageInBubble, expectMcpToolImage, expectPngInRequest, expectToolRowImage, runToolImageTurn, writeToolImage } from '../helpers/toolImages'
import { chatScrollContainer, openWorkspace, toolCallRow } from '../helpers/ui'
import { piTest } from '../pi-fixtures'
import { writePiMcpConfiguration } from './mcpConfiguration'
import { readPiMcpResult } from './mcpResult'
import { nativeContext, PI_AGENT } from './scenarios'
import { withMockPiModel } from './scriptedModel'

piTest('shows the picture returned by Read', async ({ native, authenticatedPiWorkspace }) => {
  const { fileName, resultRequest } = await runToolImageTurn(native, {
    workingDir: authenticatedPiWorkspace.workingDir,
    marker: 'pi',
    toolCall: image => readToolCall(native.provider, 'read-image-probe', image.path),
  })
  // The mock model takes image input, so the read tool gives the PNG to the next model request.
  expectPngInRequest(resultRequest)
  await expectToolRowImage(native.page, fileName)
})

piTest('shows one decoded native MCP image and resource image after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const directory = newProviderWorkingDir(PI_AGENT, 'pi-native-mcp-images-')
  const imageName = writeToolImage(directory, 'native-pi-mcp')
  const image = writeMcpImageServer(directory, imageName)
  const resource = writeMcpResultServer(directory, { receiptLog: join(directory, 'resource-receipt.json'), imagePath: join(directory, imageName) })
  writePiMcpConfiguration(directory, getGlobalState().tmpDir, { [image.name]: image, [resource.name]: resource })
  await withMockPiModel(directory, leapmuxServer, async (settings) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await openAgentViaAPI(leapmuxServer, context.workspaceId, directory, { agentProvider: AgentProvider.PI, ...settings })
    await openWorkspace(page, context.workspaceId)
    const calls = [
      mcpToolCall(AgentProvider.PI, 'native-pi-mcp-image', { server: image.name, tool: 'show', input: {} }),
      piMcpResourceToolCall('native-pi-resource-image', { operation: 'read', server: resource.name, uri: 'probe://image' }),
    ]
    const imageData = readFileSync(join(directory, imageName)).toString('base64')
    const resourceResult = toolCallRow(page, 'native-pi-resource-image')
    const images = chatScrollContainer(page).locator('button[aria-label="Open image"]:visible img:visible')
    for (const call of calls) {
      await runNativeToolTurn(context, { toolCalls: [call], prompt: `Read the native image ${call.id}.`, answer: `The native image ${call.id} reached the model.` })
      const native = await readPiMcpResult(context, call.id, call.name)
      expect(native.failed).toBe(false)
      if (call.id === 'native-pi-mcp-image') {
        expect(native.result.structuredContent).toEqual({ content: [{ type: 'text', text: `MCP image ${imageName}` }, { type: 'image', mimeType: 'image/png', data: imageData }] })
        await expectMcpToolImage(page, imageName, call.id)
      }
      else {
        expect(native.result.structuredContent).toEqual({ server: resource.name, uri: 'probe://image', contents: [{ uri: 'probe://image', mimeType: 'image/png', blob: imageData }] })
        await expect(resourceResult).toHaveCount(1)
        await expectDecodedImageInBubble(resourceResult)
      }
    }
    await expect(images).toHaveCount(2)
    await page.reload()
    await openWorkspace(page, context.workspaceId)
    await expect(images).toHaveCount(2)
    await expectMcpToolImage(page, imageName, 'native-pi-mcp-image')
    await expectDecodedImageInBubble(resourceResult)
  })
})
