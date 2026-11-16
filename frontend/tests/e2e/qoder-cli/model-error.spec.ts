import { expect } from '@playwright/test'
import { exerciseModelError, nativeErrorMarker } from '../helpers/nativeModelError'
import { nativeTextStep } from '../helpers/nativeScenario'
import { assistantBubbles, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { qoderTest } from '../qoder-fixtures'

qoderTest('shows the native model error and runs a later valid turn', async ({ native }) => {
  await exerciseModelError(native, {
    queueAfterFailure: 'running',
    // qodercli 1.1.65 replaces the message of an HTTP status error with its own
    // text ("The request could not be completed. Please try again."), so that
    // message never reaches the transcript. Qoder relays the provider's own
    // words only for a stream that fails after a partial answer, in the
    // `errors` of its `result`.
    error: {
      status: 500,
      code: 'stream_error',
      message: nativeErrorMarker(),
      midStream: true,
    },
  })
})

qoderTest('recovers from a transient native error without publishing a retry event', async ({ native }) => {
  // Qoder 1.1.86 retries below its SDK event bridge and reports no per-attempt frame.
  const { page, modelScript } = native
  const answer = 'QODER_TRANSIENT_ERROR_RECOVERED'
  const firstStep = await modelScript.queue(
    {
      error: { status: 503, message: 'The service is unavailable.' },
    },
    nativeTextStep(native, answer),
  )
  await sendMessage(page, modelScript.prompt('Run the native transient error probe.'))
  await modelScript.waitForSteps(firstStep + 2)
  await waitForAgentIdle(page)
  const requests = (await modelScript.status()).requests.filter(request => request.stepIndex === firstStep || request.stepIndex === firstStep + 1)
  expect(requests.map(request => request.stepIndex)).toEqual([firstStep, firstStep + 1])
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForAgentIdle(page)
    }
    await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
    await expect(messageContents(page).filter({ hasText: answer })).toHaveText(answer)
    await expect(messageContents(page).filter({ hasText: /API retry/ })).toHaveCount(0)
    await expect(page.locator('[data-testid="result-divider"]:visible').filter({ hasText: /failed/i })).toHaveCount(0)
  }
})
