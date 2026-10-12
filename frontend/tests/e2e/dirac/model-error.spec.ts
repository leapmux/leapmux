import { expect } from '@playwright/test'
import { diracTest } from '../dirac-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'
import { answerControl, expectNoControlBanner, waitForControlBanner } from '../helpers/ui'

// Dirac 0.5.17 retries a failed model request three times. Only the card of the
// last attempt states the service error, and Dirac then asks whether to retry
// ("API Request Failed": Retry or Cancel). The turn stays open until the reader
// answers, and Cancel ends it.
const DIRAC_REQUEST_ATTEMPTS = 4

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
