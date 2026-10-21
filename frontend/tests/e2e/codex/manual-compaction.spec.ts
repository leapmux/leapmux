import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { compactionIndicator, compactionNoticeRows, expectCompactionNotice, expectCompactionNoticeAfterReload, expectNoLiveCompaction } from '../helpers/compaction'
import { exerciseFailedNativeCompaction, exerciseNativeCompaction, MANUAL_COMPACTION_SUMMARY } from '../helpers/manualCompaction'
import { messageContents, waitForAgentIdle } from '../helpers/ui'
import { CODEX_COMPACTION, exerciseCodexCompaction } from './compactionScenario'

codexTest.describe('Codex compaction notice', () => {
  codexTest('a manual compaction draws the notice and removes old context', async ({ native }) => {
    await exerciseCodexCompaction(native)
    await expectCompactionNotice(native.page)
  })

  codexTest('a manual compaction shows a live row while it runs and stores only its end', async ({ native }) => {
    const summary = CODEX_COMPACTION.summary
    if (summary.route !== 'rule')
      throw new Error('The Codex compaction scenario needs a summarizer rule.')
    const gate = 'codex-compaction-hold'
    await withCleanup(async () => {
      const outcomes = await Promise.allSettled([
        exerciseNativeCompaction(native, { ...CODEX_COMPACTION, summary: { ...summary, respond: { text: MANUAL_COMPACTION_SUMMARY, gate } } }),
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

  codexTest('a failed manual compaction draws one failure row and no spinner', async ({ native }) => {
    await exerciseFailedNativeCompaction(native, CODEX_COMPACTION)
    for (const reload of [false, true]) {
      if (reload) {
        await native.page.reload()
        await waitForAgentIdle(native.page)
      }
      await expect(messageContents(native.page).filter({ hasText: 'Compaction failed' })).toHaveCount(1)
      await expect(messageContents(native.page).filter({ hasText: 'Turn failed' })).toHaveCount(1)
      await expect(messageContents(native.page).filter({ hasText: 'Compacting context...' })).toHaveCount(0)
    }
    await expectNoLiveCompaction(native.page)
  })
})
