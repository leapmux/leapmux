import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { mcpServersConfig } from '../helpers/mcpProbeServer'
import { nativeMcpRefusal, readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelToolNames, nativeTextStep } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { lettaMcpCliToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, expectNoControlBanner, openWorkspace, sendMessage, tabById, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { newProviderWorkingDir, openProviderAgent } from '../helpers/workspace'
import { lettaTest } from '../letta-fixtures'
import { mcpLettaTest, withRegisteredLettaMcp } from './fixtures'
import { exerciseLettaMcpCatalog } from './mcpScenario'
import { LETTA_AGENT, nativeContext } from './scenarios'

lettaTest('offers no project MCP input route in the actual native tool catalog', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const workingDir = newProviderWorkingDir(LETTA_AGENT, 'letta-code-project-form-')
  const receiptLog = join(workingDir, 'project-form-receipt.json')
  const server = writeMcpFormServer(workingDir, 'project-form.mjs', { receiptLog })
  writeFileSync(join(workingDir, '.mcp.json'), JSON.stringify(mcpServersConfig(server)))
  const { agentId } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, LETTA_AGENT, { workingDir })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  await tabById(page, agentId).click()
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const answer = 'The native project capability probe completed.'
  await expectNoNativeControl(context, {
    testId: 'elicitation-form',
    additionalTestIds: ['control-banner'],
    relatedProof: async () => {
      const request = await sendNativeAnswer(context, 'Return one actual native answer from the configured project.', answer)
      expect(nativeModelToolNames(request).some(name => name.includes(server.name))).toBe(false)
    },
  })
  expect(existsSync(receiptLog)).toBe(false)
  await page.reload()
  // The answer shows that the transcript loaded again, so a form count of zero describes the restored view.
  await expect(assistantBubbles(page).filter({ hasText: answer }).first()).toBeVisible()
  await expect(page.getByTestId('elicitation-form')).toHaveCount(0)
})

mcpLettaTest('returns the actual registered native MCP form refusal without an input control', async ({ privateMcpLettaWorkspace, page, modelScript }) => {
  const workspace = privateMcpLettaWorkspace
  const context = await nativeContext({ page, modelScript, leapmuxServer: workspace.server, workspaceId: workspace.workspaceId })
  await sendNativeAnswer(context, 'Create one actual native conversation before form registration.', 'The native form conversation exists.')
  const receiptLog = join(workspace.runDirectory, 'form-receipt.json')
  const server = writeMcpFormServer(workspace.runDirectory, 'form-server.mjs', { receiptLog })
  await withRegisteredLettaMcp(context, workspace, [server], async (identity) => {
    const catalog = await exerciseLettaMcpCatalog(context, workspace, identity.agentId, server.name)
    const tool = catalog.find(tool => tool.name === `mcp__${server.name}__ask`)
    expect(tool).toMatchObject({ inputSchema: { type: 'object', properties: {}, additionalProperties: false } })
    const toolId = tool?.name
    if (typeof toolId !== 'string')
      throw new Error('The actual form catalog contains no native ask tool ID.')
    await expectNoNativeControl(context, {
      testId: 'elicitation-form',
      additionalTestIds: ['control-banner'],
      relatedProof: async () => {
        const callId = 'letta-registered-native-form'
        const answer = 'The native form refusal reached its agent.'
        const start = await modelScript.queue({ toolCalls: [lettaMcpCliToolCall(callId, identity.agentId, toolId, {})] }, nativeTextStep(context, answer))
        await sendMessage(page, modelScript.prompt('Invoke the actual registered form tool once.'))
        await modelScript.waitForSteps(start + 2)
        const receipt = readMcpServerReceipt(receiptLog)
        const refusal = nativeMcpRefusal(receipt)
        expect(receipt.initializeCapabilities).toEqual({})
        expect(refusal.reply.error).toEqual({ code: -32601, message: 'Method not found' })
        expect(refusal.request.id).toBe(refusal.reply.id)
        expect(refusal.request.toolRequestId).toBe(refusal.toolResult.id)
        expect(refusal.toolResult.isError).toBe(true)
        expect(nativeToolResult(await modelScript.requestAt(start + 1), callId)).toContain(refusal.toolResult.text)
        await waitForAgentIdle(page)
        await expect(assistantBubbles(page).filter({ hasText: answer }).first()).toBeVisible()
      },
    })
    await page.reload()
    await waitForSettingsHydrated(page)
    await expect(page.getByTestId('elicitation-form')).toHaveCount(0)
    await expectNoControlBanner(page)
  })
})
