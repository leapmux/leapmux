import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { writeMcpImageServer } from '../helpers/mcpImageServer'
import { mcpToolCall, readToolCall } from '../helpers/providerToolCalls'
import { expectMcpToolImage, expectToolRowWithoutImage, writeToolImage } from '../helpers/toolImages'
import { expectSettingsOptionChosen, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { KIRO_E2E_SKIP_REASON, kiroTest, openKiroAgent } from '../kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

const KIRO = AgentProvider.KIRO

kiroTest.describe('Kiro images in tool results', () => {
  // Allow All lets the scripted calls run without permission requests. LeapMux owns this preset because Kiro does not report it.
  // Kiro's native Read returns image metadata without image bytes. The call identifies the file.
  // This case verifies that native Read limitation. The following MCP case verifies an actual image result and its rendering.
  kiroTest('a Read of a PNG runs and draws no picture in the tool row', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { policyPreset: 'allow-all' })
    const name = writeToolImage(workingDir, 'kiro-58')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsOptionChosen(page, 'policyPreset-allow-all')

    await modelScript.queue(
      { toolCalls: [readToolCall(KIRO, 'read-png', join(workingDir, name))] },
      { text: `I opened ${name}.` },
    )
    await sendMessage(page, modelScript.prompt(`Read the file ${name} and describe it.`))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectToolRowWithoutImage(page, 'tool-image-kiro-58')
  })

  kiroTest('renders the image returned by a local MCP tool', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    let imageName = ''
    let ready = ''
    await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { policyPreset: 'allow-all' }, (workingDir) => {
      imageName = writeToolImage(workingDir, 'kiro-mcp')
      const server = writeMcpImageServer(workingDir, imageName)
      ready = server.ready
      const settings = join(workingDir, '.kiro', 'settings')
      mkdirSync(settings, { recursive: true })
      writeFileSync(join(settings, 'mcp.json'), JSON.stringify({ mcpServers: { image_probe: { command: process.execPath, args: server.args } } }))
    })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expect.poll(() => existsSync(ready)).toBe(true)

    const callID = 'show-kiro-image'
    await modelScript.queue(
      { toolCalls: [mcpToolCall(KIRO, callID, { server: 'image_probe', tool: 'show', input: {} })] },
      { text: 'The MCP tool returned an image.' },
    )
    await sendMessage(page, modelScript.prompt('Call the image_probe show tool.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(JSON.stringify(status.requests.find(request => request.stepIndex === 1)?.body)).toContain(`MCP image ${imageName}`)
    await expectMcpToolImage(page, imageName, callID)
  })
})
