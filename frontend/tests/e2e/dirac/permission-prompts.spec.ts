import type { Page } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { DIRAC_E2E_SKIP_REASON, diracTest, expect } from '../dirac-fixtures'
import { bashToolCall, diracRespondToolCall } from '../helpers/providerToolCalls'
import { messageContents, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

diracTest.describe('Dirac control requests', () => {
  diracTest.skip(!!DIRAC_E2E_SKIP_REASON, DIRAC_E2E_SKIP_REASON || '')

  const PROVIDER = AgentProvider.DIRAC

  function banner(page: Page) {
    return page.getByTestId('control-banner').filter({ visible: true })
  }

  async function chatText(page: Page): Promise<string> {
    return (await messageContents(page).allTextContents()).join(' ')
  }

  diracTest('runs a command after the reader approves it', async ({ askingDiracWorkspace, page, modelScript }) => {
    void askingDiracWorkspace
    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'dirac-allow', 'echo "dirac-allow-$(printf 42)"')] },
      { toolCalls: [diracRespondToolCall('dirac-allow-done', 'complete', 'The command ran.')] },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted command.'))
    // The call waits on the banner, so the second step waits too.
    await modelScript.waitForSteps(1)

    await waitForControlBanner(page)
    await expect(banner(page)).toContainText('dirac-allow')
    await expect(page.getByTestId('control-allow-btn').filter({ visible: true })).toHaveText('Allow')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    await expect(banner(page)).toHaveCount(0)
    // The command computes the marker, so only a run prints it.
    await expect.poll(() => chatText(page)).toContain('dirac-allow-42')
  })

  diracTest('keeps the command from running after the reader rejects it', async ({ askingDiracWorkspace, page, modelScript }) => {
    void askingDiracWorkspace
    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'dirac-deny', 'echo "dirac-deny-$(printf 42)"')] },
      { toolCalls: [diracRespondToolCall('dirac-deny-done', 'complete', 'I did not run it.')] },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted command.'))
    await modelScript.waitForSteps(1)

    await waitForControlBanner(page)
    await expect(page.getByTestId('control-deny-btn').filter({ visible: true })).toHaveText('Deny')
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    await expect(banner(page)).toHaveCount(0)
    // A denied call reaches no shell, so its output marker is nowhere on the
    // page. The command text itself never states the number below.
    await expect.poll(() => chatText(page)).not.toContain('dirac-deny-42')
  })
})
