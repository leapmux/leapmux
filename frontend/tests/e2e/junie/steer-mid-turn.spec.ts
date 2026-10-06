import { junieAnswerToolCall } from '../helpers/providerToolCalls'
import { queuedInputRow, steerButton } from '../helpers/steer'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expect, junieTest } from '../junie-fixtures'

junieTest.describe('junie unsupported controls', () => {
  junieTest('runs a steered prompt in the next turn', async ({ authenticatedJunieWorkspace, page, modelScript }) => {
    void authenticatedJunieWorkspace
    const firstPrompt = modelScript.prompt('Answer the first request.')
    const secondPrompt = modelScript.prompt('Answer the second request after the first.')
    const firstGate = 'junie-first-turn'
    await modelScript.queue(
      { gate: firstGate, toolCalls: [junieAnswerToolCall('junie-first-answer', 'FIRST_JUNIE_ANSWER')] },
      { toolCalls: [junieAnswerToolCall('junie-second-answer', 'SECOND_JUNIE_ANSWER')] },
    )

    await sendMessage(page, firstPrompt)
    await modelScript.waitForGate(firstGate)
    try {
      await sendMessage(page, secondPrompt)
      const queued = queuedInputRow(page, 'Answer the second request')
      await expect(queued).toBeVisible()
      await expect(steerButton(queued)).toBeVisible()
      await steerButton(queued).click()
      expect((await modelScript.status()).requests.some(request => request.stepIndex === 1)).toBe(false)
    }
    finally {
      if ((await modelScript.status()).pendingGates.includes(firstGate))
        await modelScript.releaseGate(firstGate)
    }

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const first = status.requests.find(request => request.stepIndex === 0)
    const second = status.requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(first?.body ?? {}).includes('Answer the second request')).toBe(false)
    expect(JSON.stringify(second?.body ?? {}).includes('Answer the second request')).toBe(true)
    await expect(assistantBubbles(page).filter({ hasText: 'FIRST_JUNIE_ANSWER' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'SECOND_JUNIE_ANSWER' }).first()).toBeVisible()
    await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(2)
  })
})
