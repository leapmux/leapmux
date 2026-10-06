import { diracTest, expect } from '../dirac-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'
import { waitForControlBanner } from '../helpers/ui'
import { nativeContext } from './scenarios'

// Dirac 0.5.17 retries a failed model request three times. Only the card of the
// last attempt states the service error, and Dirac then asks whether to retry
// ("API Request Failed": Retry or Cancel). The turn stays open until the reader
// answers, and Cancel ends it.
const DIRAC_REQUEST_ATTEMPTS = 4

diracTest('shows the native model error and runs a later valid turn', async ({ authenticatedDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  await exerciseModelError(context, {
    queueAfterFailure: 'running',
    attempts: DIRAC_REQUEST_ATTEMPTS,
    answerFailure: async (error) => {
      const banner = await waitForControlBanner(page)
      await expect(banner).toContainText(error.message)
      await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
      await expect(page.getByTestId('control-banner').filter({ visible: true })).toHaveCount(0)
    },
  })
})
