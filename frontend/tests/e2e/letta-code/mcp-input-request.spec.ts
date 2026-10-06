import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { nativeMcpRefusal, readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { lettaMcpCliToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { assistantBubbles, openWorkspace, sendMessage, tabById, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { lettaTest } from '../letta-fixtures'
import { mcpLettaTest, withRegisteredLettaMcp } from './fixtures'
import { exerciseLettaMcpCatalog } from './mcpScenario'
import { nativeContext } from './scenarios'

lettaTest('offers no project MCP input route in the actual native tool catalog', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const workingDir = createTestDirectory('letta-code-project-form-')
  const receiptLog = join(workingDir, 'project-form-receipt.json')
  const script = writeMcpFormServer(workingDir, 'project-form.mjs', { receiptLog })
  writeFileSync(join(workingDir, '.mcp.json'), JSON.stringify({ mcpServers: { form_probe: { command: process.execPath, args: [script] } } }))
  const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, workingDir, {
    agentProvider: AgentProvider.LETTA,
    ...agentOpenOptions(agentSettings(AgentProvider.LETTA)),
  })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  await tabById(page, agentId).click()
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const request = await sendNativeAnswer(context, 'Return one actual native answer from the configured project.', 'The native project capability probe completed.')
  expect(nativeModelToolNames(request).some(name => name.includes('form_probe'))).toBe(false)
  expect(existsSync(receiptLog)).toBe(false)
  await expect(page.locator('[data-testid="elicitation-form"]:visible')).toHaveCount(0)
  await page.reload()
  await expect(page.locator('[data-testid="elicitation-form"]:visible')).toHaveCount(0)
})

mcpLettaTest('returns the actual registered native MCP form refusal without an input control', async ({ privateMcpLettaWorkspace, page, modelScript }) => {
  const workspace = privateMcpLettaWorkspace
  const context = await nativeContext({ page, modelScript, leapmuxServer: workspace.server, workspaceId: workspace.workspaceId })
  await sendNativeAnswer(context, 'Create one actual native conversation before form registration.', 'The native form conversation exists.')
  const receiptLog = join(workspace.runDirectory, 'form-receipt.json')
  const script = writeMcpFormServer(workspace.runDirectory, 'form-server.mjs', { receiptLog })
  await withRegisteredLettaMcp(context, workspace, [{ name: 'form_probe', transport: 'stdio', command: workspace.nodeExecutable, args: [script] }], async (identity) => {
    const catalog = await exerciseLettaMcpCatalog(context, workspace, identity.agentId, 'form_probe')
    const tool = catalog.find(tool => tool.name === 'mcp__form_probe__ask')
    expect(tool).toMatchObject({ inputSchema: { type: 'object', properties: {}, additionalProperties: false } })
    const toolId = tool?.name
    if (typeof toolId !== 'string')
      throw new Error('The actual form catalog contains no native ask tool ID.')
    await expectNoNativeControl(context, {
      testId: 'elicitation-form',
      additionalTestIds: ['control-banner'],
      relatedProof: async () => {
        const start = (await modelScript.status()).stepCount
        const callId = 'letta-registered-native-form'
        const answer = 'The native form refusal reached its agent.'
        await modelScript.queue({ toolCalls: [lettaMcpCliToolCall(callId, identity.agentId, toolId, {})] }, { text: answer })
        await sendMessage(page, modelScript.prompt('Invoke the actual registered form tool once.'))
        const status = await modelScript.waitForSteps(start + 2)
        const receipt = readMcpServerReceipt(receiptLog)
        const refusal = nativeMcpRefusal(receipt)
        expect(receipt.initializeCapabilities).toEqual({})
        expect(refusal.reply.error).toEqual({ code: -32601, message: 'Method not found' })
        expect(refusal.request.id).toBe(refusal.reply.id)
        expect(refusal.request.toolRequestId).toBe(refusal.toolResult.id)
        expect(refusal.toolResult.isError).toBe(true)
        expect(nativeToolResult(status.requests.find(record => record.stepIndex === start + 1), callId)).toContain(refusal.toolResult.text)
        await waitForAgentIdle(page)
        await expect(assistantBubbles(page).filter({ hasText: answer }).first()).toBeVisible()
      },
    })
    await page.reload()
    await waitForSettingsHydrated(page)
    await expect(page.locator('[data-testid="elicitation-form"]:visible')).toHaveCount(0)
    await expect(page.locator('[data-testid="control-banner"]:visible')).toHaveCount(0)
  })
})
