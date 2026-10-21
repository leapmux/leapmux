import { expect } from '@playwright/test'
import { ERROR_MARKER } from '../../../src/components/chat/assembledMessage'
import { exerciseModelError, nativeErrorMarker } from '../helpers/nativeModelError'
import { messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { mimoTest } from '../mimo-fixtures'

mimoTest('shows the native model failure and accepts the next valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

mimoTest('marks the partial text that MiMo restores after a failed stream', async ({ native }) => {
  const { page, modelScript } = native
  const message = nativeErrorMarker()
  const step = await modelScript.queue({ error: { status: 400, code: 'invalid_request_error', message, midStream: true } })
  await sendMessage(page, modelScript.prompt('Run the scripted stream failure.'))
  await modelScript.waitForSteps(step + 1)
  for (const reload of [false, true]) {
    if (reload)
      await page.reload()
    await waitForAgentIdle(page)
    await expect(messageContents(page).filter({ hasText: message })).toHaveCount(1)
    await expect(page.locator('[data-testid="result-divider"]:visible').filter({ hasText: message })).toHaveCount(1)
    await expect(messageContents(page).filter({ hasText: 'partial' })).toHaveCount(1)
    await expect(messageContents(page).filter({ hasText: 'partial' })).toContainText(ERROR_MARKER)
  }
})
