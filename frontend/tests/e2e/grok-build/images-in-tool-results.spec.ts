import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { grokTest } from '../grok-fixtures'
import { writeMcpImageServer } from '../helpers/mcpImageServer'
import { mcpServersConfig } from '../helpers/mcpProbeServer'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { mcpToolCall, readToolCall } from '../helpers/providerToolCalls'
import { newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { expectMcpToolImage, expectToolRowWithoutImage, runToolImageTurn, writeToolImage } from '../helpers/toolImages'
import { answerControl, expectNoControlBanner, expectSettingsOptionChosen, openWorkspace, waitForControlBanner } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { GROK_AGENT, nativeContext } from './scenarios'

grokTest.describe('Grok Build images in tool results', () => {
  // Always Approve lets the scripted calls run without permission requests. LeapMux owns this option because Grok does not report it.
  // Grok Build 1.0.41 returns "Cannot read binary file" for a valid PNG. Its native Read description promises image reads.
  // This case verifies that native Read limitation. The following MCP case verifies an actual image result and its rendering.
  grokTest('a Read of a PNG runs and draws no picture in the tool row', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    const { workingDir } = await openProviderAgent(leapmuxServer, context.workspaceId, GROK_AGENT, { optionValues: { approvalMode: 'always-approve' } })
    await openWorkspace(page, context.workspaceId)
    await expectSettingsOptionChosen(page, 'approvalMode-always-approve')
    await runToolImageTurn(context, {
      workingDir,
      marker: 'grok-21',
      toolCall: image => readToolCall(context.provider, 'read-png', image.path),
    })
    await expectToolRowWithoutImage(page, 'tool-image-grok-21')
  })

  grokTest('renders the image returned by a local MCP tool', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    const workingDir = newProviderWorkingDir(GROK_AGENT)
    const imageName = writeToolImage(workingDir, 'grok-mcp')
    const server = writeMcpImageServer(workingDir, imageName)
    writeFileSync(join(workingDir, '.mcp.json'), JSON.stringify(mcpServersConfig(server)))
    await openProviderAgent(leapmuxServer, context.workspaceId, GROK_AGENT, { workingDir, optionValues: { approvalMode: 'always-approve' } })
    await openWorkspace(page, context.workspaceId)

    await expect(await waitForControlBanner(page)).toContainText('Trust the workspace')
    await answerControl(page, 'allow')
    await expectNoControlBanner(page)
    await expect.poll(() => existsSync(server.ready)).toBe(true)

    const callID = 'show-grok-image'
    const { resultRequest } = await runNativeToolTurn(context, {
      toolCalls: [mcpToolCall(context.provider, callID, { server: server.name, tool: 'show', input: {} })],
      prompt: `Call the ${server.name} show tool.`,
      answer: 'The MCP tool returned an image.',
    })
    expect(JSON.stringify(resultRequest.body)).toContain(`MCP image ${imageName}`)
    await expectMcpToolImage(page, imageName, callID)
  })
})
