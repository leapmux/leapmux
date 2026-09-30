import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { writeMcpImageServer } from './helpers/mcpImageServer'
import { mcpToolCall, readToolCall } from './helpers/providerToolCalls'
import { expectMcpToolImage, expectToolRowWithoutImage, writeToolImage } from './helpers/toolImages'
import { expectSettingsOptionChosen, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, KIRO_E2E_SKIP_REASON, kiroTest, openKiroAgent } from './kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

const KIRO = AgentProvider.KIRO

kiroTest.describe('Kiro images in tool results', () => {
  // The agent runs the Allow all policy, so no permission request stands between
  // the scripted call and the row this test reads. The policy is LeapMux's own
  // option, because Kiro never reports its preset.
  //
  // Kiro's read answers an image with metadata text (path, format, size) and no
  // picture. The call runs and names the file. When Kiro returns image content,
  // flip this to `expectToolRowImage` -- LeapMux already renders that result
  // (see 306 and 308).
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
    await waitForAgentIdle(page, 120_000)

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
    await waitForAgentIdle(page, 120_000)
    expect(JSON.stringify(status.requests.find(request => request.stepIndex === 1)?.body)).toContain(`MCP image ${imageName}`)
    await expectMcpToolImage(page, imageName, callID)
  })
})
