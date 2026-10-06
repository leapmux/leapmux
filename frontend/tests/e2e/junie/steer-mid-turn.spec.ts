import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { nativeTextStep } from '../helpers/nativeScenario'
import { queuedInputRow, steerButton } from '../helpers/steer'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { junieTest } from '../junie-fixtures'

junieTest.describe('junie unsupported controls', () => {
  junieTest('runs a steered prompt in the next turn', async ({ native }) => {
    const { page, modelScript } = native
    const firstPrompt = modelScript.prompt('Answer the first request.')
    const secondPrompt = modelScript.prompt('Answer the second request after the first.')
    const firstGate = 'junie-first-turn'
    const start = await modelScript.queue(
      { ...nativeTextStep(native, 'FIRST_JUNIE_ANSWER'), gate: firstGate },
      nativeTextStep(native, 'SECOND_JUNIE_ANSWER'),
    )

    await sendMessage(page, firstPrompt)
    await modelScript.waitForGate(firstGate)
    await withCleanup(async () => {
      await sendMessage(page, secondPrompt)
      const queued = queuedInputRow(page, 'Answer the second request')
      await expect(queued).toBeVisible()
      await expect(steerButton(queued)).toBeVisible()
      await steerButton(queued).click()
      expect((await modelScript.status()).requests.some(request => request.stepIndex === start + 1)).toBe(false)
    }, async () => {
      await modelScript.releaseGateIfHeld(firstGate)
    })

    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    expect(JSON.stringify((await modelScript.requestAt(start)).body)).not.toContain('Answer the second request')
    expect(JSON.stringify((await modelScript.requestAt(start + 1)).body)).toContain('Answer the second request')
    await expect(assistantBubbles(page).filter({ hasText: 'FIRST_JUNIE_ANSWER' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'SECOND_JUNIE_ANSWER' }).first()).toBeVisible()
    await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(2)
  })
})
