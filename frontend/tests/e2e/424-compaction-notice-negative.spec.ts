import { expect, test } from './fixtures'
import { COMPACTION_NOTICE_TEXT, compactionNoticeRow } from './helpers/compaction'
import { assistantBubbles, sendMessage, waitForAgentIdle } from './helpers/ui'

test('does not treat assistant text as a completed compaction notice', async ({ authenticatedWorkspace, page, modelScript }) => {
  void authenticatedWorkspace
  await modelScript.queue({ text: COMPACTION_NOTICE_TEXT })
  await sendMessage(page, modelScript.prompt('Reply once with the scripted response.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)

  await expect(assistantBubbles(page).filter({ hasText: COMPACTION_NOTICE_TEXT }).first()).toBeVisible()
  await expect(compactionNoticeRow(page)).toHaveCount(0)
})
