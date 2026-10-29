import { expect } from '@playwright/test'
import { diracTest } from '../dirac-fixtures'
import { exerciseModelError, exerciseTransientModelFailure } from '../helpers/nativeModelError'
import { answerControl, assistantBubbles, expectNoControlBanner, messageContents, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

// Dirac 0.5.17 retries a failed model request three times. Only the card of the
// last attempt states the service error, and Dirac then asks whether to retry
// ("API Request Failed": Retry or Cancel). The turn stays open until the reader
// answers, and Cancel ends it.
const DIRAC_REQUEST_ATTEMPTS = 4

// Dirac 0.5.23 sends a 5xx three times in each of those four attempts: its
// OpenAI client retries the request twice before Dirac's own loop sees it.
const DIRAC_SERVER_ERROR_REQUESTS = 12

diracTest('shows the native model error and runs a later valid turn', async ({ native }) => {
  await exerciseModelError(native, {
    queueAfterFailure: 'running',
    attempts: DIRAC_REQUEST_ATTEMPTS,
    answerFailure: async (error) => {
      const banner = await waitForControlBanner(native.page)
      await expect(banner).toContainText(error.message)
      await answerControl(native.page, 'deny')
      await expectNoControlBanner(native.page)
    },
  })
})

diracTest('keeps the retry notice out of the answer text', async ({ native }) => {
  await exerciseModelError(native, {
    queueAfterFailure: 'running',
    attempts: DIRAC_REQUEST_ATTEMPTS,
    answerFailure: async (error) => {
      const banner = await waitForControlBanner(native.page)
      await expect(banner).toContainText(error.message)
      await answerControl(native.page, 'deny')
      await expectNoControlBanner(native.page)
    },
  })
  // The retry notice is its own status row, worded by the reader; the model's
  // answer carries none of Dirac's notice text.
  await expect(assistantBubbles(native.page).filter({ hasText: 'Retrying API request...' })).toHaveCount(0)
  await expect(messageContents(native.page).filter({ hasText: 'Retrying the model request' }).first()).toBeVisible()
})

diracTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ native }) => {
  await exerciseTransientModelFailure(native, {
    error: { status: 500, code: 'server_error', message: 'The server had an error while processing your request.' },
    attempts: DIRAC_SERVER_ERROR_REQUESTS,
    // LeapMux declines Dirac's retry question for a server error itself. The
    // question never reaches the reader, and the turn ends without an answer.
    answerFailure: async () => {
      await waitForAgentIdle(native.page)
      await expectNoControlBanner(native.page)
    },
  })
  await expectNoControlBanner(native.page)
})
