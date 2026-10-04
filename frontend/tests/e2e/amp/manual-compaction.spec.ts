import type { Page } from '@playwright/test'
import type { ModelScript } from '../helpers/modelScriptFixture'
import { expect } from '@playwright/test'
import { AMP_E2E_SKIP_REASON, ampTest } from '../amp-fixtures'
import { compactionNoticeRow } from '../helpers/compaction'
import { assistantBubbles, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'

/** Verify that the installed provider receives the slash command as model text. */
async function proveNoNativeManualCompaction(page: Page, modelScript: ModelScript): Promise<void> {
  await modelScript.queue(
    { text: 'The earlier context is present.' },
    { text: 'The slash command reached the model as text.' },
  )
  await sendMessage(page, modelScript.prompt('Create context before the compact command.'))
  await modelScript.waitForSteps(1)
  await waitForAgentIdle(page)
  await expect(userBubbles(page).filter({ hasText: 'Create context before the compact command.' }).first()).toBeVisible()
  await expect(page.locator('[data-testid="agent-input-queue"]:visible')).toHaveCount(0)

  await sendMessage(page, '/compact')
  const status = await modelScript.waitForSteps(2)
  expect(JSON.stringify(status.requests.find(request => request.stepIndex === 1)?.body)).toContain('/compact')
  await waitForAgentIdle(page)
  await expect(assistantBubbles(page).filter({ hasText: 'The slash command reached the model as text.' }).first()).toBeVisible()
  await expect(compactionNoticeRow(page)).toHaveCount(0)
}

ampTest.describe('Amp manual compaction', () => {
  ampTest.skip(!!AMP_E2E_SKIP_REASON, AMP_E2E_SKIP_REASON || '')

  ampTest('passes the slash command to the model in stream mode', async ({ authenticatedAmpWorkspace, page, modelScript }) => {
    void authenticatedAmpWorkspace
    await proveNoNativeManualCompaction(page, modelScript)
  })
})
