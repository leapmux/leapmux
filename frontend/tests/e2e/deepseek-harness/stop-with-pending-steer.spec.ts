import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseStopWithPendingSteer } from '../helpers/nativeToolSteering'
import { messageContents, waitForAgentIdle } from '../helpers/ui'

deepseekHarnessTest('removes an unread native steer after a stop and sends it once from the queue', async ({ native }) => {
  await exerciseStopWithPendingSteer(native, { rowAt: 'acceptance' })
  for (const reload of [false, true]) {
    if (reload) {
      await native.page.reload()
      await waitForAgentIdle(native.page)
    }
    await expect(messageContents(native.page).filter({ hasText: 'Message queued again' })).toHaveCount(1)
  }
})
