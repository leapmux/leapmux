import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { createGrokWorkingDir, expect, GROK_E2E_SKIP_REASON, grokTest, openGrokAgent } from './grok-fixtures'
import { openAgentViaAPI } from './helpers/api'
import { writeMcpImageServer } from './helpers/mcpImageServer'
import { mcpToolCall, readToolCall } from './helpers/providerToolCalls'
import { expectMcpToolImage, expectToolRowWithoutImage, writeToolImage } from './helpers/toolImages'
import { expectSettingsOptionChosen, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

const GROK = AgentProvider.GROK_BUILD

grokTest.describe('Grok Build images in tool results', () => {
  // The agent runs Always Approve, so no permission request stands between the
  // scripted call and the row this test reads. The approval mode is LeapMux's
  // own option, because Grok never reports it.
  //
  // The shipped Grok Build 1.0.41 answers "Cannot read binary file" for a valid
  // PNG although its read_file description promises image reads. The call runs
  // and names the file; no picture is drawn. When the CLI returns image
  // content, flip this to `expectToolRowImage` -- LeapMux already renders that
  // result (see 306 and 308).
  grokTest('a Read of a PNG runs and draws no picture in the tool row', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openGrokAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { approvalMode: 'always-approve' })
    const name = writeToolImage(workingDir, 'grok-21')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsOptionChosen(page, 'approvalMode-always-approve')

    await modelScript.queue(
      { toolCalls: [readToolCall(GROK, 'read-png', join(workingDir, name))] },
      { text: `I opened ${name}.` },
    )
    await sendMessage(page, modelScript.prompt(`Read the file ${name} and describe it.`))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expectToolRowWithoutImage(page, 'tool-image-grok-21')
  })

  grokTest('renders the image returned by a local MCP tool', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const workingDir = createGrokWorkingDir()
    const imageName = writeToolImage(workingDir, 'grok-mcp')
    const server = writeMcpImageServer(workingDir, imageName)
    writeFileSync(join(workingDir, '.mcp.json'), JSON.stringify({ mcpServers: { image_probe: { command: server.command, args: server.args } } }))
    const settings = agentOpenOptions(agentSettings(GROK))
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, workingDir, {
      agentProvider: GROK,
      ...settings,
      optionValues: { ...settings.optionValues, approvalMode: 'always-approve' },
    })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('Trust the workspace')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await expect(banner).toHaveCount(0)
    await expect.poll(() => existsSync(server.ready)).toBe(true)

    const callID = 'show-grok-image'
    await modelScript.queue(
      { toolCalls: [mcpToolCall(GROK, callID, { server: 'image_probe', tool: 'show', input: {} })] },
      { text: 'The MCP tool returned an image.' },
    )
    await sendMessage(page, modelScript.prompt('Call the image_probe show tool.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    expect(JSON.stringify(status.requests.find(request => request.stepIndex === 1)?.body)).toContain(`MCP image ${imageName}`)
    await expectMcpToolImage(page, imageName, callID)
  })
})
