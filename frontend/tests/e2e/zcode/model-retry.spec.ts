import { expect } from '@playwright/test'
import { selectedAgentTabId } from '../helpers/nativeScenario'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { waitForTerminalReady } from '../helpers/terminal'
import { assistantBubbles, chatScrollContainer, messageContents, openTerminalViaUI, sendMessage, tabById, visibleOnly, waitForAgentIdle } from '../helpers/ui'
import { listTerminalsViaAPI } from '../helpers/workerTabs'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('folds a retried request into one resolved retry row', async ({ native }) => {
  const { page, modelScript } = native
  const agentId = await selectedAgentTabId(page)
  const historyMarker = 'Earlier chat history'
  const historyText = Array.from({ length: 80 }, (_, index) => `${historyMarker} ${index + 1}`).join('\n\n')
  const historyStep = await modelScript.queue({ text: historyText })
  await sendMessage(page, modelScript.prompt('Return the scripted history.'))
  await modelScript.waitForSteps(historyStep + 1)
  await waitForAgentIdle(page)
  await expect(assistantBubbles(page).filter({ hasText: historyMarker })).toHaveCount(1)
  await expect.poll(() => chatScrollContainer(page).evaluate(list => list.scrollHeight > list.clientHeight)).toBe(true)

  const retryGate = 'zcode-retry-response'
  const start = await modelScript.queue(
    {
      error: { status: 500, message: 'probe' },
    },
    { text: 'RETRY_OK', gate: retryGate },
  )
  const indicator = visibleOnly(page.getByTestId('model-api-retry-indicator'))
  try {
    await sendMessage(page, modelScript.prompt('Return the scripted answer.'))
    await modelScript.waitForGate(retryGate)
    await expect(indicator).toContainText(/Model API retry 2(?:\/\d+)? .* Running/)
    await expect(indicator).toBeInViewport()

    const terminalId = await openTerminalViaUI(page)
    const { hubUrl, adminToken, workerId } = native.leapmuxServer
    await retryUntilPass(async () => {
      const terminals = await listTerminalsViaAPI(hubUrl, adminToken, workerId, native.workspaceId)
      expect(terminals.map(terminal => terminal.id)).toContain(terminalId)
    })
    await expect(tabById(page, agentId)).toHaveAttribute('aria-selected', 'false')
    await expect(tabById(page, terminalId)).toHaveAttribute('aria-selected', 'true')
    // Terminal content requires FULL interest. The Worker applies agent interest first.
    await waitForTerminalReady(page)
    await expect(indicator).toHaveCount(0)

    await tabById(page, agentId).click()
    expect(await selectedAgentTabId(page)).toBe(agentId)
    await expect(indicator).toContainText(/Model API retry 2(?:\/\d+)? .* Running/)
    await expect(indicator).toBeInViewport()

    await modelScript.releaseGate(retryGate)
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    await expect(indicator).toHaveCount(0)
  }
  finally {
    await modelScript.releaseGateIfHeld(retryGate)
  }
  for (const reload of [false, true]) {
    if (reload)
      await page.reload()
    await expect(assistantBubbles(page).filter({ hasText: 'RETRY_OK' })).toHaveCount(1)
    const retry = messageContents(page).filter({ hasText: 'API retry' })
    await expect(retry).toHaveCount(1)
    await expect(retry).toContainText('succeeded')
  }
})
