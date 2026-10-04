import { expect } from '@playwright/test'
import { GEMINI_E2E_SKIP_REASON, geminiTest } from '../gemini-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { releaseNativeTurnGate } from '../helpers/nativeLifecycle'
import { queuedInputRow, steerButton } from '../helpers/steer'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'

geminiTest.skip(!!GEMINI_E2E_SKIP_REASON, GEMINI_E2E_SKIP_REASON || '')

geminiTest('keeps a steer request in the input queue until the native turn ends', async ({ page, modelScript, authenticatedGeminiWorkspace }) => {
  void authenticatedGeminiWorkspace
  const gate = 'gemini-no-native-steer'
  await modelScript.queue({ gate, text: 'The first native turn completed.' }, { text: 'The queued native turn completed.' })
  await withCleanup(async () => {
    await sendMessage(page, modelScript.prompt('Complete the first held native turn.'))
    await modelScript.waitForGate(gate)
    await sendMessage(page, modelScript.prompt('Process QUEUED_GEMINI_INPUT after the active turn.'))
    const queued = queuedInputRow(page, 'Process QUEUED_GEMINI_INPUT')
    await expect(queued).toBeVisible()
    await steerButton(queued).click()
    expect((await modelScript.status()).requests.some(row => row.stepIndex === 1)).toBe(false)
    await releaseNativeTurnGate(modelScript, gate)
    const status = await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)
    expect(JSON.stringify(status.requests.find(row => row.stepIndex === 0)?.body)).not.toContain('QUEUED_GEMINI_INPUT')
    expect(JSON.stringify(status.requests.find(row => row.stepIndex === 1)?.body)).toContain('QUEUED_GEMINI_INPUT')
    await expect(assistantBubbles(page).filter({ hasText: 'The first native turn completed.' })).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'The queued native turn completed.' })).toBeVisible()
    await expect(queued).toHaveCount(0)
  }, () => releaseNativeTurnGate(modelScript, gate))
})
