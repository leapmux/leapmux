import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { writeMcpImageServer } from '../helpers/mcpImageServer'
import { mcpServersConfig } from '../helpers/mcpProbeServer'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelToolNames, nativeTextStep } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { lettaMcpCliToolCall } from '../helpers/providerToolCalls'
import { writeToolImage } from '../helpers/toolImages'
import { assistantBubbles, openWorkspace, sendMessage, tabById, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { newProviderWorkingDir, openProviderAgent } from '../helpers/workspace'
import { lettaTest } from '../letta-fixtures'
import { mcpLettaTest, withRegisteredLettaMcp } from './fixtures'
import { exerciseLettaMcpCatalog } from './mcpScenario'
import { LETTA_AGENT, nativeContext } from './scenarios'

lettaTest.describe('native mcp tool execution', () => {
  lettaTest('offers no local MCP tool to the model on its App Server path', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const workingDir = newProviderWorkingDir(LETTA_AGENT, 'letta-mcp-')
    const imageName = writeToolImage(workingDir, 'letta-mcp')
    const server = writeMcpImageServer(workingDir, imageName)
    writeFileSync(join(workingDir, '.mcp.json'), JSON.stringify(mcpServersConfig(server)))
    const { agentId } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, LETTA_AGENT, { workingDir })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await tabById(page, agentId).click()
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    // The reader of the tool names fails for an empty catalog, so the check below reads a real catalog.
    const tools = nativeModelToolNames(await sendNativeAnswer(context, 'Reply once.', 'The native turn ended.'))
    expect(tools.some(name => name.startsWith('mcp__') || name.includes(server.name))).toBe(false)
    expect(existsSync(server.ready)).toBe(false)
  })
})

mcpLettaTest('executes a registered native MCP CLI tool and preserves an empty input after reload', async ({ privateMcpLettaWorkspace, page, modelScript }) => {
  const workspace = privateMcpLettaWorkspace
  const context = await nativeContext({ page, modelScript, leapmuxServer: workspace.server, workspaceId: workspace.workspaceId })
  const original = 'The private MCP conversation exists before registration.'
  await sendNativeAnswer(context, 'Create the native conversation for the MCP registration proof.', original)
  const receiptLog = join(workspace.runDirectory, 'echo-receipt.json')
  const server = writeMcpEchoServer(workspace.runDirectory, { receiptLog })
  await withRegisteredLettaMcp(context, workspace, [server], async (identity) => {
    await expect(assistantBubbles(page).filter({ hasText: original })).toHaveCount(1)
    const catalog = await exerciseLettaMcpCatalog(context, workspace, identity.agentId, server.name)
    expect(catalog).toHaveLength(1)
    expect(catalog[0]).toMatchObject({ name: `mcp__${server.name}__echo`, inputSchema: { type: 'object', required: ['value'], properties: { value: { type: 'string' } } } })
    const toolId = catalog[0]?.name
    if (typeof toolId !== 'string')
      throw new Error('The actual echo catalog contains no native tool ID.')
    for (const [index, value] of ['NATIVE_REGISTERED_ECHO42', ''].entries()) {
      if (index === 1) {
        await page.reload()
        await waitForSettingsHydrated(page)
      }
      const callId = `letta-mcp-echo-${index}`
      const answer = `The native echo case ${index} completed.`
      const start = await modelScript.queue({ toolCalls: [lettaMcpCliToolCall(callId, identity.agentId, toolId, { value })] }, nativeTextStep(context, answer))
      await sendMessage(page, modelScript.prompt(`Execute the actual registered echo case ${index}.`))
      await modelScript.waitForSteps(start + 2)
      const receipt = readMcpServerReceipt(receiptLog)
      expect(receipt.initializeCapabilities).not.toBeNull()
      // Each native CLI process opens its own MCP client and writes a new server receipt.
      expect(receipt.toolResults).toEqual([{ id: expect.anything(), tool: 'echo', text: `MCP_ECHO:${value}`, isError: false }])
      const result = nativeToolResult(await modelScript.requestAt(start + 1), callId)
      expect(result).toContain(`MCP_ECHO:${value}`)
      if (value === '')
        expect(result).not.toContain('NATIVE_REGISTERED_ECHO42')
      expect(receipt.elicitationRequests).toEqual([])
      await waitForAgentIdle(page)
      await expect(assistantBubbles(page).filter({ hasText: answer }).first()).toBeVisible()
    }
  })
})
