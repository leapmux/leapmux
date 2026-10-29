import { join } from 'node:path'
import { expect } from '@playwright/test'
import { escapeRegExp } from '../../../src/lib/regexp'
import { copilotTest } from '../copilot-fixtures'
import { writeMcpSamplingServer } from '../helpers/mcpSamplingServer'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelToolNames, nativeTextStep } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { controlActions, expectNoControlBanner, sendMessage, toolCallRow, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { withCopilotMcpServer } from './mcpScenario'

for (const decision of ['Allow', 'Deny'] as const) {
  copilotTest(`answers a native MCP sampling request with ${decision}`, async ({ authenticatedEmptyWorkspace, leapmuxServer, modelScript, page }, testInfo) => {
    const fixtures = {
      page,
      leapmuxServer,
      modelScript,
      workspaceId: authenticatedEmptyWorkspace.workspaceId,
    }
    const samplingPrompt = 'Reply with the single word SAMPLED.'
    const samplingRule = `copilot-sampling-${decision.toLowerCase()}`
    await modelScript.rule({
      name: samplingRule,
      when: {
        user: escapeRegExp(samplingPrompt),
      },
      respond: { text: 'SAMPLED' },
    })
    let receiptLog = ''
    await withCopilotMcpServer(fixtures, (directory) => {
      receiptLog = join(directory, 'sampling-receipt.json')
      return writeMcpSamplingServer(directory, {
        prompt: modelScript.prompt(samplingPrompt),
        receiptLog,
      })
    }, async (native, server) => {
      const catalog = await sendNativeAnswer(native, 'Confirm the local sampling server is ready.', 'The sampling server is ready.')
      const callId = `sampling-tool-${decision.toLowerCase()}`
      const tool = mcpToolCall(native.provider, callId, { server: server.name, tool: 'sample', input: {} })
      expect(nativeModelToolNames(catalog)).toContain(tool.name)
      const start = await modelScript.queue({
        toolCalls: [tool],
      }, nativeTextStep(native, 'The sampling tool completed.'))
      await sendMessage(page, modelScript.prompt('Call the local sampling tool once.'))
      await modelScript.waitForSteps(start + 1)
      const banner = await waitForControlBanner(page)
      await expect(banner).toContainText(`The MCP server ${server.name} asks the model to answer`)
      await controlActions(page).getByRole('button', { name: decision, exact: true }).click()
      await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      const result = nativeToolResult(await modelScript.requestAt(start + 1), callId)
      const expected = decision === 'Allow'
        ? 'SAMPLED'
        : 'MCP_SAMPLING_REFUSED'
      expect(result).toContain(expected)
      const receipt = readMcpServerReceipt(receiptLog)
      await testInfo.attach('mcp-sampling-receipt', {
        body: JSON.stringify(receipt, null, 2),
        contentType: 'application/json',
      })
      expect(receipt.toolResults).toHaveLength(1)
      expect(receipt.toolResults[0]?.text).toContain(expected)
      expect((await modelScript.status()).ruleMatches[samplingRule] ?? 0).toBe(decision === 'Allow'
        ? 1
        : 0)
      await expect(toolCallRow(page, callId).filter({ hasText: expected }).first()).toBeVisible()
      await expectNoControlBanner(page)
      await page.reload()
      await waitForAgentIdle(page)
      await expectNoControlBanner(page)
    })
  })
}
