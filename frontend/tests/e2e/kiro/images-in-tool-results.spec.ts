import { existsSync } from 'node:fs'
import { expect } from '@playwright/test'
import { MCP_IMAGE_SERVER_NAME, writeMcpImageServer } from '../helpers/mcpImageServer'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { mcpToolCall, readToolCall } from '../helpers/providerToolCalls'
import { expectMcpToolImage, expectToolRowWithoutImage, runToolImageTurn, writeToolImage } from '../helpers/toolImages'
import { expectSettingsOptionChosen, openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { kiroTest } from '../kiro-fixtures'
import { writeKiroProjectMcpServers } from './mcpConfiguration'
import { KIRO_AGENT, nativeContext } from './scenarios'

kiroTest.describe('Kiro images in tool results', () => {
  // Allow All lets the scripted calls run without permission requests. LeapMux owns this preset because Kiro does not report it.
  // Kiro's native Read returns image metadata without image bytes. The call identifies the file.
  // This case verifies that native Read limitation. The following MCP case verifies an actual image result and its rendering.
  kiroTest('a Read of a PNG runs and draws no picture in the tool row', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    const { workingDir } = await openProviderAgent(leapmuxServer, context.workspaceId, KIRO_AGENT, { optionValues: { policyPreset: 'allow-all' } })
    await openWorkspace(page, context.workspaceId)
    await expectSettingsOptionChosen(page, 'policyPreset-allow-all')
    await runToolImageTurn(context, {
      workingDir,
      marker: 'kiro-58',
      toolCall: image => readToolCall(context.provider, 'read-png', image.path),
    })
    await expectToolRowWithoutImage(page, 'tool-image-kiro-58')
  })

  kiroTest('renders the image returned by a local MCP tool', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    let imageName = ''
    let ready = ''
    await openProviderAgent(leapmuxServer, context.workspaceId, KIRO_AGENT, { optionValues: { policyPreset: 'allow-all' }, prepare: (workingDir) => {
      imageName = writeToolImage(workingDir, 'kiro-mcp')
      const server = writeMcpImageServer(workingDir, imageName)
      ready = server.ready
      writeKiroProjectMcpServers(workingDir, server)
    } })
    await openWorkspace(page, context.workspaceId)
    await expect.poll(() => existsSync(ready)).toBe(true)

    const callID = 'show-kiro-image'
    const { resultRequest } = await runNativeToolTurn(context, {
      toolCalls: [mcpToolCall(context.provider, callID, { server: MCP_IMAGE_SERVER_NAME, tool: 'show', input: {} })],
      prompt: `Call the ${MCP_IMAGE_SERVER_NAME} show tool.`,
      answer: 'The MCP tool returned an image.',
    })
    expect(JSON.stringify(resultRequest.body)).toContain(`MCP image ${imageName}`)
    await expectMcpToolImage(page, imageName, callID)
  })
})
