import { expect } from '@playwright/test'
import { test } from './fixtures'
import { COMPACTION_NOTICE_TEXT, compactionNoticeRow } from './helpers/compaction'
import { sendScriptedTurn } from './helpers/scriptedTurn'

test('does not treat assistant text as a completed compaction notice', async ({ authenticatedWorkspace, page, modelScript }) => {
  void authenticatedWorkspace
  // The helper requires the answer in an assistant bubble, so the text did render.
  await sendScriptedTurn(page, modelScript, { prompt: 'Reply once with the scripted response.', answer: COMPACTION_NOTICE_TEXT })

  await expect(compactionNoticeRow(page)).toHaveCount(0)
})

test('accepts completed Worker status and rejects incomplete or assistant status text', async ({ page }) => {
  await page.setContent(`
    <main data-chat-scroll-container="true">
      <div data-testid="message-bubble" data-role="agent">Context compacted</div>
      <div data-testid="message-bubble" data-role="leapmux">Compacting the context</div>
      <div data-testid="message-bubble" data-role="leapmux" hidden>Context compacted</div>
    </main>
  `)
  await expect(compactionNoticeRow(page)).toHaveCount(0)
  await page.evaluate(() => {
    const row = document.createElement('div')
    row.dataset.testid = 'message-bubble'
    row.dataset.role = 'leapmux'
    row.textContent = 'Context compacted from 1000 to 200 tokens'
    document.querySelector('main')!.append(row)
  })
  await expect(compactionNoticeRow(page)).toHaveText('Context compacted from 1000 to 200 tokens')
})
