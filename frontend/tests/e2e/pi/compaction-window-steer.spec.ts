import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { compactionIndicator, expectCompactionNoticeAfterReload, expectNoLiveCompaction } from '../helpers/compaction'
import { MANUAL_COMPACTION_SUMMARY } from '../helpers/manualCompaction'
import { nativeTextStep } from '../helpers/nativeScenario'
import { queuedInputRow, steerQueuedInput } from '../helpers/steer'
import { assistantBubbles, chatScrollContainer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { piTest } from '../pi-fixtures'
import { PI_COMPACTION } from './compactionScenario'

for (const mode of ['steer', 'queue'] as const) {
  piTest(`accepts ${mode} input while automatic compaction holds the session`, async ({ native }) => {
    const { page, modelScript } = native
    const gate = `pi-compaction-${mode}`
    const answer = `The ${mode} input reached the model after compaction.`
    const guidance = `Continue with the ${mode} input after compaction.`
    // Pi chooses the retained text by its actual size, independently of reported usage.
    const initialAnswer = 'The first answer fills the context window. '.repeat(20)
    if (PI_COMPACTION.summary.route !== 'rule')
      throw new Error('The Pi compaction scenario needs its native summarizer rule.')
    await modelScript.rule({
      name: `pi-window-summary-${mode}`,
      when: PI_COMPACTION.summary.when,
      respond: { text: MANUAL_COMPACTION_SUMMARY, gate },
    })
    await withCleanup(async () => {
      const start = await modelScript.queue(
        { ...nativeTextStep(native, initialAnswer), usage: { inputTokens: 120000, outputTokens: 220 } },
        nativeTextStep(native, answer),
      )
      await sendMessage(page, modelScript.prompt('Use the scripted context window.'))
      await modelScript.waitForGate(gate)
      await expect(compactionIndicator(page)).toBeVisible()
      await expect(page.locator('[data-testid="thinking-indicator"]:visible')).toBeVisible()
      if (mode === 'steer') {
        await steerQueuedInput(page, { message: modelScript.prompt(guidance), match: guidance })
      }
      else {
        await sendMessage(page, modelScript.prompt(guidance))
        await expect(queuedInputRow(page, guidance)).toBeVisible()
      }
      await modelScript.releaseGate(gate)
      await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
      await expect(chatScrollContainer(page)).not.toContainText('prompt failed')
      await expect(chatScrollContainer(page).locator('[data-testid="result-divider"]:visible')).toHaveCount(mode === 'steer' ? 1 : 2)
      await expectCompactionNoticeAfterReload(page, { count: 1, divider: true })
      await expectNoLiveCompaction(page)
    }, async () => { await modelScript.releaseGateIfHeld(gate) })
  })
}
