import { expect } from '@playwright/test'
import { MCP_IMAGE_SERVER_NAME, writeMcpImageServer } from '../helpers/mcpImageServer'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { mcpToolCall, reasonixViewImageToolCall } from '../helpers/providerToolCalls'
import { expectDecodedImageInBubble, expectImageDataUriInRequest, expectToolRowImage, runToolImageTurn, writeToolImage } from '../helpers/toolImages'
import { toolCallRow } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'
import { openReasonixMcpAgent } from './mcpScenario'

reasonixTest('recovers the native stored image bytes into the tool result and keeps them after reload', async ({ native, authenticatedReasonixWorkspace }) => {
  const callId = 'reasonix-view-image'
  const { fileName, resultRequest } = await runToolImageTurn(native, {
    workingDir: authenticatedReasonixWorkspace.workingDir,
    marker: 'reasonix',
    toolCall: image => reasonixViewImageToolCall(callId, image.path),
  })
  expectImageDataUriInRequest(resultRequest, 'image/png')
  for (const reload of [false, true]) {
    if (reload)
      await native.page.reload()
    const result = toolCallRow(native.page, callId)
    await expect(result).toContainText(fileName)
    await expectToolRowImage(native.page, fileName)
    await expect(result.locator('button[aria-label="Open image"]')).toHaveCount(1)
  }
})

reasonixTest('renders a native MCP image behind its provenance header before and after reload', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  let imageName = ''
  const context = await openReasonixMcpAgent({ page, workspaceId: authenticatedEmptyWorkspace.workspaceId, leapmuxServer, modelScript }, (directory) => {
    imageName = writeToolImage(directory, 'reasonix-mcp')
    return writeMcpImageServer(directory, imageName)
  })
  const callId = 'reasonix-mcp-image'
  const { resultRequest } = await runNativeToolTurn(context, {
    toolCalls: [mcpToolCall(context.provider, callId, { server: MCP_IMAGE_SERVER_NAME, tool: 'show', input: {} })],
    prompt: 'Run the scripted image tool once.',
    answer: 'The native MCP image arrived.',
  })
  expectImageDataUriInRequest(resultRequest, 'image/png')
  for (const reload of [false, true]) {
    if (reload)
      await page.reload()
    const result = toolCallRow(page, callId)
    await expect(result).toContainText(imageName)
    await expectDecodedImageInBubble(result)
  }
})
