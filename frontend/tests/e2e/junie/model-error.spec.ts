import { expect } from '@playwright/test'
import { exerciseModelError, exerciseTransientModelFailure } from '../helpers/nativeModelError'
import { messageContents } from '../helpers/ui'
import { junieTest } from '../junie-fixtures'

junieTest('shows the native model error and runs a later valid turn', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

junieTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ native }) => {
  // Junie's HTTP client sends a 5xx six times. Junie then states the failure as
  // a sessionFailure of category `unknown` that offers a retry, and cancels the turn.
  const message = 'The server had an error while processing your request.'
  await exerciseTransientModelFailure(native, {
    error: { status: 500, code: 'server_error', message },
    attempts: 6,
    // Junie writes no text of its own for a sessionFailure. LeapMux states it as
    // an agent-error notice row, the reader's only record of the failure.
    answerFailure: async () => {
      await expect(messageContents(native.page).filter({ hasText: message })).toHaveCount(1)
    },
  })
  await expect(messageContents(native.page).filter({ hasText: message })).toHaveCount(1)
})
