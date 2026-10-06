import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { writeMcpImageServer } from '../helpers/mcpImageServer'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { mcpToolCall, readToolCall } from '../helpers/providerToolCalls'
import { expectMcpToolImage, expectToolRowWithoutImage, runToolImageTurn, writeToolImage } from '../helpers/toolImages'
import { expectSettingsOptionChosen, openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { KIRO_AGENT, kiroTest } from '../kiro-fixtures'
import { nativeContext } from './scenarios'

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
      const settings = join(workingDir, '.kiro', 'settings')
      mkdirSync(settings, { recursive: true })
      writeFileSync(join(settings, 'mcp.json'), JSON.stringify({ mcpServers: { image_probe: { command: process.execPath, args: server.args } } }))
    } })
    await openWorkspace(page, context.workspaceId)
    await expect.poll(() => existsSync(ready)).toBe(true)

    const callID = 'show-kiro-image'
    const { resultRequest } = await runNativeToolTurn(context, {
      toolCalls: [mcpToolCall(context.provider, callID, { server: 'image_probe', tool: 'show', input: {} })],
      prompt: 'Call the image_probe show tool.',
      answer: 'The MCP tool returned an image.',
    })
    expect(JSON.stringify(resultRequest.body)).toContain(`MCP image ${imageName}`)
    await expectMcpToolImage(page, imageName, callID)
  })
})
