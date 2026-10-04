import { expect } from '@playwright/test'
import { CLINE_E2E_SKIP_REASON, clineTest } from '../cline-fixtures'
import { compactionNoticeRow } from '../helpers/compaction'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'

clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

clineTest('sends the slash command as model text without a native compaction', async ({ authenticatedClineWorkspace, page, modelScript }) => {
  void authenticatedClineWorkspace
  await modelScript.queue({ text: 'The earlier task is complete.' }, { text: 'I received the literal slash command.' })
  await sendMessage(page, modelScript.prompt('Record the first turn.'))
  await modelScript.waitForSteps(1)
  await waitForAgentIdle(page)

  await sendMessage(page, '/compact')
  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  const request = status.requests.find(candidate => candidate.stepIndex === 1)
  expect(JSON.stringify(request?.body)).toContain('/compact')
  await expect(assistantBubbles(page).filter({ hasText: 'I received the literal slash command.' }).first()).toBeVisible()
  await expect(compactionNoticeRow(page)).toHaveCount(0)
})
