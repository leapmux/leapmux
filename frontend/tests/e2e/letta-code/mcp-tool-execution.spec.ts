import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { writeMcpImageServer } from '../helpers/mcpImageServer'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { lettaMcpCliToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { writeToolImage } from '../helpers/toolImages'
import { assistantBubbles, loginViaToken, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { expect, lettaTest } from '../letta-fixtures'
import { mcpLettaTest, withRegisteredLettaMcp } from './fixtures'
import { exerciseLettaMcpCatalog } from './mcpScenario'
import { nativeContext } from './scenarios'

lettaTest.describe('native mcp tool execution', () => {
  lettaTest('offers no local MCP tool to the model on its App Server path', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const workingDir = createTestDirectory('letta-mcp-')
    const imageName = writeToolImage(workingDir, 'letta-mcp')
    const server = writeMcpImageServer(workingDir, imageName)
    writeFileSync(join(workingDir, '.mcp.json'), JSON.stringify({
      mcpServers: { image_probe: { command: server.command, args: server.args } },
    }))
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, workingDir, agentOpenOptions(AgentProvider.LETTA))
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await modelScript.queue({ text: 'The native turn ended.' })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    const request = status.requests.find(record => record.stepIndex === 0)
    if (!request)
      throw new Error('The actual project discovery turn reached no model request.')
    const tools = nativeModelToolNames(request)
    expect(tools.length).toBeGreaterThan(0)
    expect(tools.some(name => name.startsWith('mcp__'))).toBe(false)
    expect(existsSync(server.ready)).toBe(false)
  })
})

mcpLettaTest('executes a registered native MCP CLI tool and preserves an empty input after reload', async ({ privateMcpLettaWorkspace, page, modelScript }) => {
  const workspace = privateMcpLettaWorkspace
  const context = await nativeContext({ page, modelScript, leapmuxServer: workspace.server, workspaceId: workspace.workspaceId })
  const original = 'The private MCP conversation exists before registration.'
  await sendNativeAnswer(context, 'Create the native conversation for the MCP registration proof.', original)
  const receiptLog = join(workspace.runDirectory, 'echo-receipt.json')
  const script = writeMcpEchoServer(workspace.runDirectory, { receiptLog })
  await withRegisteredLettaMcp(context, workspace, [{ name: 'echo_probe', transport: 'stdio', command: workspace.nodeExecutable, args: [script] }], async (identity) => {
    await expect(assistantBubbles(page).filter({ hasText: original })).toHaveCount(1)
    const catalog = await exerciseLettaMcpCatalog(context, workspace, identity.agentId, 'echo_probe')
    expect(catalog).toHaveLength(1)
    expect(catalog[0]).toMatchObject({ name: 'mcp__echo_probe__echo', inputSchema: { type: 'object', required: ['value'], properties: { value: { type: 'string' } } } })
    const toolId = catalog[0]?.name
    if (typeof toolId !== 'string')
      throw new Error('The actual echo catalog contains no native tool ID.')
    for (const [index, value] of ['NATIVE_REGISTERED_ECHO42', ''].entries()) {
      if (index === 1) {
        await page.reload()
        await waitForSettingsHydrated(page)
      }
      const start = (await modelScript.status()).stepCount
      const callId = `letta-mcp-echo-${index}`
      const answer = `The native echo case ${index} completed.`
      await modelScript.queue({ toolCalls: [lettaMcpCliToolCall(callId, identity.agentId, toolId, { value })] }, { text: answer })
      await sendMessage(page, modelScript.prompt(`Execute the actual registered echo case ${index}.`))
      const status = await modelScript.waitForSteps(start + 2)
      const receipt = readMcpServerReceipt(receiptLog)
      expect(receipt.initializeCapabilities).not.toBeNull()
      // Each native CLI process opens its own MCP client and writes a new server receipt.
      expect(receipt.toolResults).toEqual([{ id: expect.anything(), tool: 'echo', text: `MCP_ECHO:${value}`, isError: false }])
      const result = nativeToolResult(status.requests.find(record => record.stepIndex === start + 1), callId)
      expect(result).toContain(`MCP_ECHO:${value}`)
      if (value === '')
        expect(result).not.toContain('NATIVE_REGISTERED_ECHO42')
      expect(receipt.elicitationRequests).toEqual([])
      await waitForAgentIdle(page)
      await expect(assistantBubbles(page).filter({ hasText: answer }).first()).toBeVisible()
    }
  })
})
