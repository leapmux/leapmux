import type { Page } from '@playwright/test'
import type { ModelScript } from '../helpers/modelScriptFixture'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider, ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { decompressContentToString } from '../../../src/lib/decompress'
import { copilotTest } from '../copilot-fixtures'
import { getTestChannel } from '../helpers/api'
import { attachCopilotNativeLogs } from '../helpers/copilotNativeLogs'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectToolRowImage, writeToolImage } from '../helpers/toolImages'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

async function proveToolImage(page: Page, modelScript: ModelScript, provider: AgentProvider, workingDir: string, approveRead = false): Promise<void> {
  const fileName = writeToolImage(workingDir, String(provider))
  await modelScript.queue(
    { toolCalls: [readToolCall(provider, 'read-image-probe', join(workingDir, fileName))] },
    { text: `I inspected ${fileName}.` },
  )
  await sendMessage(page, modelScript.prompt(`Read ${fileName} and describe it.`))
  if (approveRead) {
    await modelScript.waitForSteps(1)
    const permission = page.getByTestId('control-banner').filter({ visible: true })
    await expect(permission).toContainText(fileName)
    await page.getByTestId('control-actions').getByRole('button', { name: 'Allow', exact: true }).click()
  }
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  await expectToolRowImage(page, fileName)
}

copilotTest('shows the picture returned by View', async ({ authenticatedCopilotWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const workingDir = authenticatedCopilotWorkspace.workingDir
  if (!workingDir)
    throw new Error('Copilot test workspace has no working directory')
  try {
    await proveToolImage(page, modelScript, AgentProvider.GITHUB_COPILOT, workingDir, true)
  }
  catch (error) {
    await attachCopilotNativeLogs(leapmuxServer.agentEnv.COPILOT_HOME, testInfo)
    try {
      const agentId = await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().getAttribute('data-tab-id') ?? ''
      const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
      const response = await channel.callWorker(
        leapmuxServer.workerId,
        'ListAgentMessages',
        ListAgentMessagesRequestSchema,
        ListAgentMessagesResponseSchema,
        { agentId, limit: 200 },
      )
      const rows = response.messages.map(message => ({
        seq: String(message.seq),
        spanType: message.spanType,
        content: decompressContentToString(message.content, message.contentCompression),
        supplemental: decompressContentToString(message.supplementalContent, message.supplementalContentCompression),
      }))
      await testInfo.attach('copilot-native-worker-rows', { body: JSON.stringify(rows, null, 2), contentType: 'application/json' })
    }
    catch (captureError) {
      await testInfo.attach('copilot-native-worker-capture-error', { body: String(captureError), contentType: 'text/plain' })
    }
    throw error
  }
})
