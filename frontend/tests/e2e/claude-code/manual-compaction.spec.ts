import { expect } from '@playwright/test'
import { claudeTest } from '../claude-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { compactionIndicator, compactionNoticeRows, expectCompactionNotice, expectCompactionNoticeAfterReload, expectNoLiveCompaction } from '../helpers/compaction'
import { CLAUDE_SUMMARIZER_PATTERN, exerciseFailedNativeCompaction, exerciseNativeCompaction, MANUAL_COMPACTION_SUMMARY } from '../helpers/manualCompaction'
import { assistantBubbles, messageContents, waitForAgentIdle } from '../helpers/ui'
import { CLAUDE_COMPACTION } from './compactionScenario'

claudeTest.describe('Claude Code compaction notice', () => {
  claudeTest('a manual compaction draws the context-compacted notice', async ({ native }) => {
    await exerciseNativeCompaction(native, CLAUDE_COMPACTION)
    await expectCompactionNotice(native.page)
  })

  claudeTest('a manual compaction shows a live row while it runs and stores only its end', async ({ native }) => {
    const gate = 'claude-compaction-hold'
    await withCleanup(async () => {
      const outcomes = await Promise.allSettled([
        exerciseNativeCompaction(native, { summary: { route: 'rule', when: { user: CLAUDE_SUMMARIZER_PATTERN }, respond: { text: MANUAL_COMPACTION_SUMMARY, gate } } }),
        (async () => {
          try {
            await native.modelScript.waitForGate(gate)
            await expect(compactionIndicator(native.page)).toBeVisible()
            await expect(compactionNoticeRows(native.page)).toHaveCount(0)
          }
          finally {
            await native.modelScript.releaseGateIfHeld(gate)
          }
        })(),
      ])
      for (const outcome of outcomes) {
        if (outcome.status === 'rejected')
          throw outcome.reason
      }
      await expectNoLiveCompaction(native.page)
      await expectCompactionNoticeAfterReload(native.page, { count: 1, divider: true })
    }, async () => { await native.modelScript.releaseGateIfHeld(gate) })
  })

  claudeTest('a failed manual compaction draws one failure row and no spinner', async ({ native }) => {
    await exerciseFailedNativeCompaction(native, CLAUDE_COMPACTION)
    for (const reload of [false, true]) {
      if (reload) {
        await native.page.reload()
        await waitForAgentIdle(native.page)
      }
      await expect(messageContents(native.page).filter({ hasText: 'Compaction failed' })).toHaveCount(1)
      await expect(messageContents(native.page).filter({ hasText: 'Compacting context...' })).toHaveCount(0)
      await expect(assistantBubbles(native.page).and(native.page.locator('[data-category="assistant_text"]')).filter({ hasText: 'Error during compaction' })).toHaveCount(0)
    }
    await expectNoLiveCompaction(native.page)
  })
})
