import type { Page } from '@playwright/test'
import type { ModelScript } from '../helpers/modelScriptFixture'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectToolRowImage, writeToolImage } from '../helpers/toolImages'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { KILO_E2E_SKIP_REASON, kiloTest } from '../kilo-fixtures'

kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')

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

kiloTest('shows the picture returned by Read', async ({ authenticatedKiloWorkspace, page, modelScript }) => {
  const workingDir = authenticatedKiloWorkspace.workingDir
  if (!workingDir)
    throw new Error('Kilo test workspace has no working directory')
  await proveToolImage(page, modelScript, AgentProvider.KILO, workingDir)
})
