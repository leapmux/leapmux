import type { Page } from '@playwright/test'
import type { ModelScript } from './helpers/modelScriptFixture'
import { AMP_E2E_SKIP_REASON, ampTest } from './amp-fixtures'
import { CURSOR_E2E_SKIP_REASON, cursorTest } from './cursor-fixtures'
import { expect } from './fixtures'
import { compactionNoticeRow } from './helpers/compaction'
import { assistantBubbles, sendMessage, userBubbles, waitForAgentIdle } from './helpers/ui'
import { REASONIX_E2E_SKIP_REASON, reasonixTest } from './reasonix-fixtures'

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

cursorTest.describe('Cursor manual compaction', () => {
  cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')
  cursorTest('passes the slash command to the model in ACP mode', async ({ authenticatedCursorWorkspace, page, modelScript }) => {
    void authenticatedCursorWorkspace
    await proveNoNativeManualCompaction(page, modelScript)
  })
})

reasonixTest.describe('Reasonix manual compaction', () => {
  reasonixTest.skip(!!REASONIX_E2E_SKIP_REASON, REASONIX_E2E_SKIP_REASON || '')
  reasonixTest('passes the slash command to the model in ACP mode', async ({ authenticatedReasonixWorkspace, page, modelScript }) => {
    void authenticatedReasonixWorkspace
    await proveNoNativeManualCompaction(page, modelScript)
  })
})

ampTest.describe('Amp manual compaction', () => {
  ampTest.skip(!!AMP_E2E_SKIP_REASON, AMP_E2E_SKIP_REASON || '')
  ampTest('passes the slash command to the model in stream mode', async ({ authenticatedAmpWorkspace, page, modelScript }) => {
    void authenticatedAmpWorkspace
    await proveNoNativeManualCompaction(page, modelScript)
  })
})
