import { expect, test } from './fixtures'
import { COMPACTION_NOTICE_TEXT, compactionNoticeRow } from './helpers/compaction'
import { sendScriptedTurn } from './helpers/scriptedTurn'

test('does not treat assistant text as a completed compaction notice', async ({ authenticatedWorkspace, page, modelScript }) => {
  void authenticatedWorkspace
  // The helper requires the answer in an assistant bubble, so the text did render.
  await sendScriptedTurn(page, modelScript, { prompt: 'Reply once with the scripted response.', answer: COMPACTION_NOTICE_TEXT })

  await expect(compactionNoticeRow(page)).toHaveCount(0)
})
