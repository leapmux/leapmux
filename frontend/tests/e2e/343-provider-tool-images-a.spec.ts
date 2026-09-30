import type { Page } from '@playwright/test'
import type { ModelScript } from './helpers/modelScriptFixture'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider, ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { decompressContentToString } from '../../src/lib/decompress'
import { isObject } from '../../src/lib/jsonPick'
import { COPILOT_E2E_SKIP_REASON, copilotTest, expect } from './copilot-fixtures'
import { getTestChannel } from './helpers/api'
import { attachCopilotNativeArtifacts } from './helpers/copilotNativeArtifacts'
import { readToolCall, zcodeNodeImageToolCall } from './helpers/providerToolCalls'
import { expectToolRowImage, writeToolImage } from './helpers/toolImages'
import { sendMessage, waitForAgentIdle } from './helpers/ui'
import { KILO_E2E_SKIP_REASON, kiloTest } from './kilo-fixtures'
import { PI_E2E_SKIP_REASON, piTest } from './pi-fixtures'
import { ZCODE_E2E_SKIP_REASON, zcodeTest } from './zcode-fixtures'

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

copilotTest.describe('Copilot images in tool results', () => {
  copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')
  copilotTest('shows the picture returned by View', async ({ authenticatedCopilotWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
    const workingDir = authenticatedCopilotWorkspace.workingDir
    if (!workingDir)
      throw new Error('Copilot test workspace has no working directory')
    try {
      await proveToolImage(page, modelScript, AgentProvider.GITHUB_COPILOT, workingDir, true)
    }
    catch (error) {
      await attachCopilotNativeArtifacts(leapmuxServer.agentEnv.COPILOT_HOME, testInfo)
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
})

kiloTest.describe('Kilo images in tool results', () => {
  kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')
  kiloTest('shows the picture returned by Read', async ({ authenticatedKiloWorkspace, page, modelScript }) => {
    const workingDir = authenticatedKiloWorkspace.workingDir
    if (!workingDir)
      throw new Error('Kilo test workspace has no working directory')
    await proveToolImage(page, modelScript, AgentProvider.KILO, workingDir)
  })
})

piTest.describe('Pi images in tool results', () => {
  piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')
  piTest('shows the picture returned by Read', async ({ authenticatedPiWorkspace, page, modelScript }) => {
    const workingDir = authenticatedPiWorkspace.workingDir
    if (!workingDir)
      throw new Error('Pi test workspace has no working directory')
    await proveToolImage(page, modelScript, AgentProvider.PI, workingDir)
  })
})

zcodeTest.describe('ZCode images in tool results', () => {
  zcodeTest.skip(!!ZCODE_E2E_SKIP_REASON, ZCODE_E2E_SKIP_REASON || '')
  zcodeTest('shows the picture emitted by the native Node tool', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
    const workingDir = authenticatedZCodeWorkspace.workingDir
    if (!workingDir)
      throw new Error('ZCode test workspace has no working directory')
    const fileName = writeToolImage(workingDir, 'zcode-native')
    const base64 = readFileSync(join(workingDir, fileName)).toString('base64')
    await modelScript.queue(
      { toolCalls: [zcodeNodeImageToolCall('zcode-node-image', base64, fileName)] },
      { text: `I inspected ${fileName}.` },
    )
    await sendMessage(page, modelScript.prompt(`Show the image result for ${fileName}.`))
    await modelScript.waitForSteps(1)
    const permission = page.getByTestId('control-banner').filter({ visible: true })
    await expect(permission).toContainText('mcp__node_repl__js')
    await page.getByTestId('control-allow-btn').click()
    const status = await modelScript.waitForSteps()
    const body = status.requests.find(request => request.stepIndex === 1)?.body
    const nativeImage = isObject(body) && Array.isArray(body.messages) && body.messages.some(message =>
      isObject(message) && message.role === 'user' && Array.isArray(message.content) && message.content.some(item =>
        isObject(item) && item.type === 'image_url' && isObject(item.image_url) && item.image_url.url === `data:image/png;base64,${base64}`,
      ),
    )
    expect(nativeImage).toBe(true)
    await waitForAgentIdle(page)
    await expectToolRowImage(page, fileName)
  })
})
